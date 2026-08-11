package orchestration

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
)

type expiryCaptureTelemetry struct {
	spans []*mockSpan
}

type expiryComponentLogger struct {
	core.NoOpLogger
	component string
}

func (logger *expiryComponentLogger) WithComponent(component string) core.Logger {
	logger.component = component
	return logger
}

func (capture *expiryCaptureTelemetry) StartSpan(ctx context.Context, name string) (context.Context, core.Span) {
	span := &mockSpan{name: name}
	capture.spans = append(capture.spans, span)
	return ctx, span
}

func (*expiryCaptureTelemetry) RecordMetric(string, float64, map[string]string) {}

type expiryProcessorFixture struct {
	mu         sync.Mutex
	checkpoint *ExecutionCheckpoint
	events     []string
	claimErr   error
	updateErr  error
	releaseErr error
	blockClaim bool
	claimDone  chan error
}

func (fixture *expiryProcessorFixture) SaveCheckpoint(context.Context, *ExecutionCheckpoint) error {
	return nil
}
func (fixture *expiryProcessorFixture) LoadCheckpoint(context.Context, string) (*ExecutionCheckpoint, error) {
	return fixture.checkpoint, nil
}
func (fixture *expiryProcessorFixture) UpdateCheckpointStatus(_ context.Context, _ string, status CheckpointStatus) error {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.events = append(fixture.events, "update:"+string(status))
	return fixture.updateErr
}
func (fixture *expiryProcessorFixture) ListPendingCheckpoints(context.Context, CheckpointFilter) ([]*ExecutionCheckpoint, error) {
	return []*ExecutionCheckpoint{fixture.checkpoint}, nil
}
func (fixture *expiryProcessorFixture) DeleteCheckpoint(context.Context, string) error { return nil }
func (fixture *expiryProcessorFixture) ClaimExpiredCheckpoints(ctx context.Context, _ ExpiredCheckpointClaimRequest) ([]*ExecutionCheckpoint, error) {
	fixture.mu.Lock()
	fixture.events = append(fixture.events, "claim")
	fixture.mu.Unlock()
	if fixture.claimErr != nil {
		return nil, fixture.claimErr
	}
	if fixture.blockClaim {
		<-ctx.Done()
		if fixture.claimDone != nil {
			fixture.claimDone <- ctx.Err()
		}
		return nil, ctx.Err()
	}
	return []*ExecutionCheckpoint{fixture.checkpoint}, nil
}
func (fixture *expiryProcessorFixture) ReleaseExpiredCheckpointClaim(context.Context, string, string) error {
	fixture.mu.Lock()
	fixture.events = append(fixture.events, "release")
	fixture.mu.Unlock()
	return fixture.releaseErr
}

func TestCheckpointExpiryProcessorDeliveryOrdering(t *testing.T) {
	for _, test := range []struct {
		name      string
		semantics DeliverySemantics
		want      []string
	}{
		{name: "at most once", semantics: DeliveryAtMostOnce, want: []string{"claim", "update:expired_rejected", "callback", "release"}},
		{name: "at least once", semantics: DeliveryAtLeastOnce, want: []string{"claim", "callback", "update:expired_rejected", "release"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := &expiryProcessorFixture{checkpoint: &ExecutionCheckpoint{
				CheckpointID: "checkpoint-1", Status: CheckpointStatusPending,
				RequestMode: RequestModeNonStreaming,
			}}
			processor, err := NewCheckpointExpiryProcessor(fixture, fixture, func(context.Context, *ExecutionCheckpoint, CommandType) {
				fixture.mu.Lock()
				fixture.events = append(fixture.events, "callback")
				fixture.mu.Unlock()
			}, ExpiryProcessorConfig{Enabled: true, ScanInterval: time.Second, BatchSize: 1, DeliverySemantics: test.semantics},
				WithCheckpointExpiryOwner("test-owner"),
			)
			if err != nil {
				t.Fatal(err)
			}
			processor.processBatch(t.Context())
			fixture.mu.Lock()
			defer fixture.mu.Unlock()
			if len(fixture.events) != len(test.want) {
				t.Fatalf("events = %#v", fixture.events)
			}
			for index := range test.want {
				if fixture.events[index] != test.want[index] {
					t.Fatalf("events = %#v, want %#v", fixture.events, test.want)
				}
			}
		})
	}
}

func TestCheckpointExpiryProcessorValidatesDependenciesAndCancellation(t *testing.T) {
	if _, err := NewCheckpointExpiryProcessor(nil, nil, nil, ExpiryProcessorConfig{}); err == nil {
		t.Fatal("nil dependencies were accepted")
	}
	fixture := &expiryProcessorFixture{}
	processor, err := NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: true, ScanInterval: time.Second},
		WithCheckpointExpiryOwner("owner"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := processor.Start(ctx); err != nil {
		t.Fatalf("canceled runnable returned error: %v", err)
	}
}

func TestCheckpointExpiryProcessorOptionsApplyAndValidate(t *testing.T) {
	fixture := &expiryProcessorFixture{}
	policy := CheckpointExpiryPolicy{
		DefaultRequestMode:   RequestModeStreaming,
		StreamingBehavior:    StreamingExpiryApplyDefault,
		NonStreamingBehavior: NonStreamingExpiryImplicitDeny,
	}
	processor, err := NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: false},
		WithCheckpointExpiryLease(17*time.Second),
		WithCheckpointExpiryPolicy(policy),
	)
	if err != nil {
		t.Fatalf("NewCheckpointExpiryProcessor() error = %v", err)
	}
	if processor.lease != 17*time.Second || processor.policy != policy {
		t.Fatalf("processor options = lease %v, policy %#v", processor.lease, processor.policy)
	}

	processor, err = NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: false},
		WithCheckpointExpiryRuntimeConfig(CheckpointExpiryRuntimeConfig{ClaimLease: 19 * time.Second}),
	)
	if err != nil {
		t.Fatalf("runtime config option error = %v", err)
	}
	if processor.lease != 19*time.Second {
		t.Fatalf("runtime-configured lease = %v", processor.lease)
	}

	if _, err := NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: false},
		WithCheckpointExpiryRuntimeConfig(CheckpointExpiryRuntimeConfig{}),
	); err == nil {
		t.Fatal("non-positive runtime-configured lease was accepted")
	}
	invalidPolicy := policy
	invalidPolicy.StreamingBehavior = StreamingExpiryBehavior("unsupported")
	if _, err := NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: false},
		WithCheckpointExpiryPolicy(invalidPolicy),
	); err == nil {
		t.Fatal("invalid checkpoint expiry policy was accepted")
	}
}

func TestCheckpointExpiryLoggerUsesOrchestrationComponent(t *testing.T) {
	fixture := &expiryProcessorFixture{}
	logger := &expiryComponentLogger{}
	processor, err := NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: false},
		WithCheckpointExpiryLogger(logger),
	)
	if err != nil {
		t.Fatal(err)
	}
	if logger.component != "framework/orchestration" || processor.logger != logger {
		t.Fatalf("expiry logger component/logger = %q/%T", logger.component, processor.logger)
	}
}

func TestCheckpointExpiryProcessorDisabledBlocksUntilCancellation(t *testing.T) {
	fixture := &expiryProcessorFixture{}
	processor, err := NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: false},
		WithCheckpointExpiryOwner("owner"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- processor.Start(ctx) }()

	select {
	case err := <-done:
		t.Fatalf("disabled runnable returned before cancellation: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("disabled runnable returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("disabled runnable did not stop after cancellation")
	}
}

func TestCheckpointExpiryProcessorBoundsEachScan(t *testing.T) {
	fixture := &expiryProcessorFixture{blockClaim: true, claimDone: make(chan error, 1)}
	processor, err := NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: true, ScanInterval: time.Second},
		WithCheckpointExpiryOwner("owner"),
	)
	if err != nil {
		t.Fatal(err)
	}
	processor.scanTimeout = 10 * time.Millisecond
	processor.processBatch(t.Context())
	select {
	case err := <-fixture.claimDone:
		if err != context.DeadlineExceeded {
			t.Fatalf("claim context error = %v, want deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("expiry scan did not enforce its timeout")
	}
}

func TestCheckpointExpiryProcessorClaimFailureIsCorrelatedAndSanitized(t *testing.T) {
	fixture := &expiryProcessorFixture{claimErr: errors.New("redis://user:secret@backend unavailable")}
	logger := &TestLogger{}
	capture := &expiryCaptureTelemetry{}
	processor, err := NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: true, ScanInterval: time.Second, BatchSize: 1},
		WithCheckpointExpiryOwner("owner"),
		WithCheckpointExpiryLogger(logger),
		WithCheckpointExpiryTelemetry(capture),
	)
	if err != nil {
		t.Fatal(err)
	}

	processor.processBatch(t.Context())

	if len(capture.spans) != 1 {
		t.Fatalf("expiry scan spans = %d, want 1", len(capture.spans))
	}
	span := capture.spans[0]
	requestID, _ := span.attributes["request_id"].(string)
	if span.name != "hitl.expiry_scan" || !span.ended || !strings.HasPrefix(requestID, "hitl-expiry-") ||
		span.attributes["status"] != "error" || len(span.errors) != 1 ||
		strings.Contains(span.errors[0].Error(), "secret") {
		t.Fatalf("expiry scan span = %#v", span)
	}
	logs := logger.GetLogsByOperation("hitl_expiry_scan")
	if len(logs) != 1 || logs[0].Level != "WARN" || logs[0].Fields["request_id"] != requestID ||
		logs[0].Fields["status"] != "error" || logs[0].Fields["error_type"] != "claim" ||
		logs[0].Fields["error"] != "backend operation failed" {
		t.Fatalf("claim failure logs = %#v", logs)
	}
	if _, ok := logs[0].Fields["duration_ms"]; !ok {
		t.Fatalf("claim failure log has no duration_ms: %#v", logs[0].Fields)
	}
}

func TestCheckpointExpiryProcessorReleaseFailureMarksScanPartial(t *testing.T) {
	fixture := &expiryProcessorFixture{
		checkpoint: &ExecutionCheckpoint{
			CheckpointID: "checkpoint-release", Status: CheckpointStatusPending,
			RequestMode: RequestModeNonStreaming,
		},
		releaseErr: errors.New("release failed with secret token"),
	}
	logger := &TestLogger{}
	capture := &expiryCaptureTelemetry{}
	processor, err := NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: true, ScanInterval: time.Second, BatchSize: 1},
		WithCheckpointExpiryOwner("owner"),
		WithCheckpointExpiryLogger(logger),
		WithCheckpointExpiryTelemetry(capture),
	)
	if err != nil {
		t.Fatal(err)
	}

	processor.processBatch(t.Context())

	if len(capture.spans) != 1 || capture.spans[0].attributes["status"] != "partial" ||
		len(capture.spans[0].errors) != 1 {
		t.Fatalf("release-failure span = %#v", capture.spans)
	}
	logs := logger.GetLogsByOperation("hitl_expiry_claim_release")
	if len(logs) != 1 || logs[0].Level != "WARN" || logs[0].Fields["status"] != "error" ||
		logs[0].Fields["error_type"] != "claim_release" || logs[0].Fields["error"] != "backend operation failed" {
		t.Fatalf("release failure logs = %#v", logs)
	}
	completion := logger.GetLogsByOperation("hitl_expiry_scan")
	if len(completion) != 1 || completion[0].Level != "INFO" || completion[0].Fields["status"] != "partial" {
		t.Fatalf("partial scan completion = %#v", completion)
	}
}

func TestCheckpointExpiryProcessorUpdateFailureMarksScanPartial(t *testing.T) {
	fixture := &expiryProcessorFixture{
		checkpoint: &ExecutionCheckpoint{
			CheckpointID: "checkpoint-update", Status: CheckpointStatusPending,
			RequestMode: RequestModeNonStreaming,
		},
		updateErr: errors.New("backend update exposed secret"),
	}
	logger := &TestLogger{}
	capture := &expiryCaptureTelemetry{}
	processor, err := NewCheckpointExpiryProcessor(fixture, fixture, nil,
		ExpiryProcessorConfig{Enabled: true, ScanInterval: time.Second, BatchSize: 1},
		WithCheckpointExpiryOwner("owner"),
		WithCheckpointExpiryLogger(logger),
		WithCheckpointExpiryTelemetry(capture),
	)
	if err != nil {
		t.Fatal(err)
	}

	processor.processBatch(t.Context())

	if len(capture.spans) != 1 || capture.spans[0].attributes["status"] != "partial" ||
		capture.spans[0].attributes["failed_count"] != 1 || len(capture.spans[0].errors) != 1 {
		t.Fatalf("update-failure span = %#v", capture.spans)
	}
	var failure *LogEntry
	for _, log := range logger.GetLogsByOperation("hitl_expiry_processor") {
		if log.Level == "WARN" && log.Fields["error_type"] == "store_write" {
			copy := log
			failure = &copy
		}
	}
	if failure == nil || failure.Fields["status"] != "error" ||
		failure.Fields["error"] != "backend operation failed" {
		t.Fatalf("update failure log = %#v", failure)
	}
}

func TestCheckpointExpiryOwnerLengthBoundary(t *testing.T) {
	fixture := &expiryProcessorFixture{}
	validOwner := strings.Repeat("a", maxCheckpointClaimOwnerLen)
	if _, err := NewCheckpointExpiryProcessor(fixture, fixture, nil, ExpiryProcessorConfig{},
		WithCheckpointExpiryOwner(validOwner)); err != nil {
		t.Fatalf("maximum-length owner rejected: %v", err)
	}
	invalidOwner := validOwner + "x"
	if _, err := NewCheckpointExpiryProcessor(fixture, fixture, nil, ExpiryProcessorConfig{},
		WithCheckpointExpiryOwner(invalidOwner)); err == nil {
		t.Fatal("over-limit owner was accepted")
	}
}

func TestLoadCheckpointExpiryRuntimeConfigFromEnvironment(t *testing.T) {
	lookup := func(name string) (string, bool) {
		if name == "TRUVAG3_HITL_EXPIRY_CLAIM_LEASE" {
			return "45s", true
		}
		return "", false
	}
	config, err := LoadCheckpointExpiryRuntimeConfigFromEnvironment(
		DefaultCheckpointExpiryRuntimeConfig(), lookup,
	)
	if err != nil {
		t.Fatal(err)
	}
	if config.ClaimLease != 45*time.Second {
		t.Fatalf("claim lease = %v, want 45s", config.ClaimLease)
	}
	if _, err := LoadCheckpointExpiryRuntimeConfigFromEnvironment(
		DefaultCheckpointExpiryRuntimeConfig(), func(string) (string, bool) { return "invalid", true },
	); err == nil {
		t.Fatal("invalid claim lease was accepted")
	}
}
