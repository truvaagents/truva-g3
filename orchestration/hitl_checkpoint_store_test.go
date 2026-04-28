package orchestration

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

// =============================================================================
// Checkpoint Store Unit Tests (with miniredis)
// =============================================================================
//
// These tests cover the Redis-dependent methods of RedisCheckpointStore
// using miniredis for isolation from a real Redis instance.
//
// Pattern follows core/schema_cache_test.go - the established framework pattern.
//
// =============================================================================

// setupCheckpointTestRedis creates a miniredis instance for checkpoint store testing
func setupCheckpointTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()

	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("Failed to start miniredis: %v", err)
	}

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	return mr, client
}

// newCheckpointTestStore creates a checkpoint store with a miniredis client for testing
func newCheckpointTestStore(t *testing.T, client *redis.Client) *RedisCheckpointStore {
	t.Helper()
	return &RedisCheckpointStore{
		client:     client,
		keyPrefix:  "test:hitl",
		ttl:        24 * time.Hour,
		redisURL:   "miniredis://test",
		logger:     &core.NoOpLogger{},
		instanceID: "test-instance",
	}
}

// isMember is a helper that handles miniredis SIsMember's two return values
func isMember(mr *miniredis.Miniredis, key, member string) bool {
	result, err := mr.SIsMember(key, member)
	if err != nil {
		return false
	}
	return result
}

// -----------------------------------------------------------------------------
// SaveCheckpoint Tests
// -----------------------------------------------------------------------------

func TestSaveCheckpoint_Success(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	cp := &ExecutionCheckpoint{
		CheckpointID:   "cp-123",
		RequestID:      "req-456",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointPlanGenerated,
		CreatedAt:      time.Now(),
		ExpiresAt:      time.Now().Add(5 * time.Minute),
	}

	err := store.SaveCheckpoint(ctx, cp)
	if err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	// Verify checkpoint was stored
	if !mr.Exists("test:hitl:checkpoint:cp-123") {
		t.Error("Checkpoint was not stored in Redis")
	}

	// Verify checkpoint was added to pending index
	if !isMember(mr, "test:hitl:pending", "cp-123") {
		t.Error("Checkpoint was not added to pending index")
	}
}

func TestSaveCheckpoint_WithRequestID_AddsToRequestIndex(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	cp := &ExecutionCheckpoint{
		CheckpointID:   "cp-123",
		RequestID:      "req-456",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointBeforeStep,
	}

	err := store.SaveCheckpoint(ctx, cp)
	if err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	// Verify request index was populated
	if !isMember(mr, "test:hitl:request:req-456", "cp-123") {
		t.Error("Checkpoint was not added to request index")
	}
}

func TestSaveCheckpoint_NonPendingStatus_SkipsPendingIndex(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	cp := &ExecutionCheckpoint{
		CheckpointID:   "cp-123",
		RequestID:      "req-456",
		Status:         CheckpointStatusApproved, // Not pending
		InterruptPoint: InterruptPointPlanGenerated,
	}

	err := store.SaveCheckpoint(ctx, cp)
	if err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	// Verify pending index was NOT used
	if isMember(mr, "test:hitl:pending", "cp-123") {
		t.Error("Non-pending checkpoint should not be added to pending index")
	}
}

// -----------------------------------------------------------------------------
// LoadCheckpoint Tests
// -----------------------------------------------------------------------------

func TestLoadCheckpoint_Success(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// Save a checkpoint first
	original := &ExecutionCheckpoint{
		CheckpointID:   "cp-123",
		RequestID:      "req-456",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointPlanGenerated,
		CreatedAt:      time.Now().Truncate(time.Second),
		ExpiresAt:      time.Now().Add(5 * time.Minute).Truncate(time.Second),
	}
	if err := store.SaveCheckpoint(ctx, original); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	// Load checkpoint
	loaded, err := store.LoadCheckpoint(ctx, "cp-123")
	if err != nil {
		t.Fatalf("LoadCheckpoint() error = %v", err)
	}

	// Verify loaded data
	if loaded.CheckpointID != original.CheckpointID {
		t.Errorf("CheckpointID = %q, want %q", loaded.CheckpointID, original.CheckpointID)
	}
	if loaded.RequestID != original.RequestID {
		t.Errorf("RequestID = %q, want %q", loaded.RequestID, original.RequestID)
	}
	if loaded.Status != original.Status {
		t.Errorf("Status = %q, want %q", loaded.Status, original.Status)
	}
}

func TestLoadCheckpoint_NotFound(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	_, err := store.LoadCheckpoint(ctx, "non-existent")
	if err == nil {
		t.Fatal("Expected error for non-existent checkpoint")
	}

	if !IsCheckpointNotFound(err) {
		t.Errorf("Expected ErrCheckpointNotFound, got: %v", err)
	}
}

// -----------------------------------------------------------------------------
// UpdateCheckpointStatus Tests
// -----------------------------------------------------------------------------

func TestUpdateCheckpointStatus_Success(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// Save a pending checkpoint
	cp := &ExecutionCheckpoint{
		CheckpointID:   "cp-123",
		RequestID:      "req-456",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointPlanGenerated,
	}
	if err := store.SaveCheckpoint(ctx, cp); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	// Verify in pending index
	if !isMember(mr, "test:hitl:pending", "cp-123") {
		t.Fatal("Checkpoint should be in pending index")
	}

	// Update status
	err := store.UpdateCheckpointStatus(ctx, "cp-123", CheckpointStatusApproved)
	if err != nil {
		t.Fatalf("UpdateCheckpointStatus() error = %v", err)
	}

	// Verify removed from pending index
	if isMember(mr, "test:hitl:pending", "cp-123") {
		t.Error("Checkpoint should be removed from pending index when status changes from pending")
	}

	// Verify status was updated
	loaded, err := store.LoadCheckpoint(ctx, "cp-123")
	if err != nil {
		t.Fatalf("LoadCheckpoint() error = %v", err)
	}
	if loaded.Status != CheckpointStatusApproved {
		t.Errorf("Status = %q, want %q", loaded.Status, CheckpointStatusApproved)
	}
}

func TestUpdateCheckpointStatus_NotFound(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	err := store.UpdateCheckpointStatus(ctx, "non-existent", CheckpointStatusApproved)
	if err == nil {
		t.Fatal("Expected error for non-existent checkpoint")
	}
}

// -----------------------------------------------------------------------------
// ListPendingCheckpoints Tests
// -----------------------------------------------------------------------------

func TestListPendingCheckpoints_Success(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// Save multiple pending checkpoints
	cp1 := &ExecutionCheckpoint{
		CheckpointID:   "cp-1",
		RequestID:      "req-1",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointPlanGenerated,
	}
	cp2 := &ExecutionCheckpoint{
		CheckpointID:   "cp-2",
		RequestID:      "req-2",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointBeforeStep,
	}

	if err := store.SaveCheckpoint(ctx, cp1); err != nil {
		t.Fatalf("SaveCheckpoint(cp1) error = %v", err)
	}
	if err := store.SaveCheckpoint(ctx, cp2); err != nil {
		t.Fatalf("SaveCheckpoint(cp2) error = %v", err)
	}

	// List pending
	checkpoints, err := store.ListPendingCheckpoints(ctx, CheckpointFilter{})
	if err != nil {
		t.Fatalf("ListPendingCheckpoints() error = %v", err)
	}

	if len(checkpoints) != 2 {
		t.Errorf("Expected 2 checkpoints, got %d", len(checkpoints))
	}
}

func TestListPendingCheckpoints_WithLimit(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// Save 3 checkpoints
	for i := 1; i <= 3; i++ {
		cp := &ExecutionCheckpoint{
			CheckpointID:   "cp-" + string(rune('0'+i)),
			Status:         CheckpointStatusPending,
			InterruptPoint: InterruptPointPlanGenerated,
		}
		if err := store.SaveCheckpoint(ctx, cp); err != nil {
			t.Fatalf("SaveCheckpoint() error = %v", err)
		}
	}

	// List with limit
	checkpoints, err := store.ListPendingCheckpoints(ctx, CheckpointFilter{Limit: 2})
	if err != nil {
		t.Fatalf("ListPendingCheckpoints() error = %v", err)
	}

	if len(checkpoints) != 2 {
		t.Errorf("Expected 2 checkpoints (limit), got %d", len(checkpoints))
	}
}

func TestListPendingCheckpoints_WithRequestIDFilter(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// Save checkpoints from different requests
	cp1 := &ExecutionCheckpoint{
		CheckpointID:   "cp-1",
		RequestID:      "req-A",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointPlanGenerated,
	}
	cp2 := &ExecutionCheckpoint{
		CheckpointID:   "cp-2",
		RequestID:      "req-B",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointPlanGenerated,
	}

	if err := store.SaveCheckpoint(ctx, cp1); err != nil {
		t.Fatalf("SaveCheckpoint(cp1) error = %v", err)
	}
	if err := store.SaveCheckpoint(ctx, cp2); err != nil {
		t.Fatalf("SaveCheckpoint(cp2) error = %v", err)
	}

	// Filter by request_id
	checkpoints, err := store.ListPendingCheckpoints(ctx, CheckpointFilter{RequestID: "req-A"})
	if err != nil {
		t.Fatalf("ListPendingCheckpoints() error = %v", err)
	}

	if len(checkpoints) != 1 {
		t.Errorf("Expected 1 checkpoint (filtered), got %d", len(checkpoints))
	}

	if len(checkpoints) > 0 && checkpoints[0].RequestID != "req-A" {
		t.Errorf("Expected checkpoint with RequestID=req-A, got %s", checkpoints[0].RequestID)
	}
}

func TestListPendingCheckpoints_Empty(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	checkpoints, err := store.ListPendingCheckpoints(ctx, CheckpointFilter{})
	if err != nil {
		t.Fatalf("ListPendingCheckpoints() error = %v", err)
	}

	if len(checkpoints) != 0 {
		t.Errorf("Expected 0 checkpoints, got %d", len(checkpoints))
	}
}

// -----------------------------------------------------------------------------
// DeleteCheckpoint Tests
// -----------------------------------------------------------------------------

func TestDeleteCheckpoint_Success(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// Save a checkpoint
	cp := &ExecutionCheckpoint{
		CheckpointID:   "cp-123",
		RequestID:      "req-456",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointPlanGenerated,
	}
	if err := store.SaveCheckpoint(ctx, cp); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	// Verify it exists
	if !mr.Exists("test:hitl:checkpoint:cp-123") {
		t.Fatal("Checkpoint should exist before delete")
	}

	// Delete it
	err := store.DeleteCheckpoint(ctx, "cp-123")
	if err != nil {
		t.Fatalf("DeleteCheckpoint() error = %v", err)
	}

	// Verify it's gone
	if mr.Exists("test:hitl:checkpoint:cp-123") {
		t.Error("Checkpoint should be deleted")
	}

	// Verify removed from pending index
	if isMember(mr, "test:hitl:pending", "cp-123") {
		t.Error("Checkpoint should be removed from pending index")
	}
}

// -----------------------------------------------------------------------------
// claimExpiredCheckpoint Tests
// -----------------------------------------------------------------------------

func TestClaimExpiredCheckpoint_Success(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	claimed, err := store.claimExpiredCheckpoint(ctx, "cp-123")
	if err != nil {
		t.Fatalf("claimExpiredCheckpoint() error = %v", err)
	}

	if !claimed {
		t.Error("Expected claim to succeed (key doesn't exist)")
	}

	// Verify claim key was set
	if !mr.Exists("test:hitl:expiry:claim:cp-123") {
		t.Error("Claim key should exist")
	}
}

func TestClaimExpiredCheckpoint_AlreadyClaimed(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// Pre-set the claim key to simulate another instance claiming it
	_ = mr.Set("test:hitl:expiry:claim:cp-123", "other-instance")

	claimed, err := store.claimExpiredCheckpoint(ctx, "cp-123")
	if err != nil {
		t.Fatalf("claimExpiredCheckpoint() error = %v", err)
	}

	if claimed {
		t.Error("Expected claim to fail (already claimed)")
	}
}

// -----------------------------------------------------------------------------
// releaseExpiredCheckpointClaim Tests
// -----------------------------------------------------------------------------

func TestReleaseExpiredCheckpointClaim_Success(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// First claim it
	_ = mr.Set("test:hitl:expiry:claim:cp-123", "test-instance")

	err := store.releaseExpiredCheckpointClaim(ctx, "cp-123")
	if err != nil {
		t.Fatalf("releaseExpiredCheckpointClaim() error = %v", err)
	}

	// Verify claim key was deleted
	if mr.Exists("test:hitl:expiry:claim:cp-123") {
		t.Error("Claim key should be deleted")
	}
}

func TestReleaseExpiredCheckpointClaim_DifferentOwner(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// Claim owned by a different instance
	_ = mr.Set("test:hitl:expiry:claim:cp-123", "other-instance")

	err := store.releaseExpiredCheckpointClaim(ctx, "cp-123")
	if err != nil {
		t.Fatalf("releaseExpiredCheckpointClaim() error = %v", err)
	}

	// Claim should NOT be deleted (different owner)
	if !mr.Exists("test:hitl:expiry:claim:cp-123") {
		t.Error("Claim key should NOT be deleted (different owner)")
	}
}

// -----------------------------------------------------------------------------
// Close Tests
// -----------------------------------------------------------------------------

func TestCheckpointStoreClose_Success(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()

	store := newCheckpointTestStore(t, client)

	err := store.Close()
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

// -----------------------------------------------------------------------------
// StopExpiryProcessor Tests
// -----------------------------------------------------------------------------

func TestCheckpointStoreStopExpiryProcessor_NotStarted(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// Should succeed even if not started
	err := store.StopExpiryProcessor(ctx)
	if err != nil {
		t.Fatalf("StopExpiryProcessor() error = %v (should be nil when not started)", err)
	}
}

// -----------------------------------------------------------------------------
// SetExpiryCallback Tests
// -----------------------------------------------------------------------------

func TestCheckpointStoreSetExpiryCallback_Success(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)

	callback := func(ctx context.Context, cp *ExecutionCheckpoint, action CommandType) {
		// Callback body - would be called during expiry processing
	}

	err := store.SetExpiryCallback(callback)
	if err != nil {
		t.Fatalf("SetExpiryCallback() error = %v", err)
	}

	// Verify callback was set
	if store.expiryCallback == nil {
		t.Error("Callback was not set")
	}
}

// -----------------------------------------------------------------------------
// Integration-style Tests (multiple operations)
// -----------------------------------------------------------------------------

func TestSaveAndLoadCheckpoint_RoundTrip(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	original := &ExecutionCheckpoint{
		CheckpointID:    "cp-roundtrip",
		RequestID:       "req-roundtrip",
		Status:          CheckpointStatusPending,
		InterruptPoint:  InterruptPointPlanGenerated,
		OriginalRequest: "test request message",
		UserContext: map[string]interface{}{
			"session_id": "sess-123",
		},
	}

	// Save
	if err := store.SaveCheckpoint(ctx, original); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	// Load
	loaded, err := store.LoadCheckpoint(ctx, "cp-roundtrip")
	if err != nil {
		t.Fatalf("LoadCheckpoint() error = %v", err)
	}

	// Verify fields
	if loaded.CheckpointID != original.CheckpointID {
		t.Errorf("CheckpointID mismatch: got %q, want %q", loaded.CheckpointID, original.CheckpointID)
	}
	if loaded.RequestID != original.RequestID {
		t.Errorf("RequestID mismatch: got %q, want %q", loaded.RequestID, original.RequestID)
	}
	if loaded.Status != original.Status {
		t.Errorf("Status mismatch: got %q, want %q", loaded.Status, original.Status)
	}
	if loaded.InterruptPoint != original.InterruptPoint {
		t.Errorf("InterruptPoint mismatch: got %q, want %q", loaded.InterruptPoint, original.InterruptPoint)
	}
}

// TestSaveAndLoadCheckpoint_RequestMode_RoundTrip verifies RequestMode survives save/load cycle.
// This is critical for HITL expiry behavior - streaming requests need implicit_deny, not apply_default.
func TestSaveAndLoadCheckpoint_RequestMode_RoundTrip(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	testCases := []struct {
		name        string
		requestMode RequestMode
	}{
		{"streaming mode", RequestModeStreaming},
		{"non_streaming mode", RequestModeNonStreaming},
		{"empty mode", RequestMode("")},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cpID := "cp-mode-" + string(tc.requestMode)
			if tc.requestMode == "" {
				cpID = "cp-mode-empty"
			}

			original := &ExecutionCheckpoint{
				CheckpointID:   cpID,
				RequestID:      "req-mode-test",
				Status:         CheckpointStatusPending,
				InterruptPoint: InterruptPointPlanGenerated,
				RequestMode:    tc.requestMode,
			}

			// Save
			if err := store.SaveCheckpoint(ctx, original); err != nil {
				t.Fatalf("SaveCheckpoint() error = %v", err)
			}

			// Load
			loaded, err := store.LoadCheckpoint(ctx, cpID)
			if err != nil {
				t.Fatalf("LoadCheckpoint() error = %v", err)
			}

			// Verify RequestMode is preserved
			if loaded.RequestMode != original.RequestMode {
				t.Errorf("RequestMode mismatch: got %q, want %q", loaded.RequestMode, original.RequestMode)
			}
		})
	}
}

func TestSaveUpdateAndListCheckpoints_Workflow(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	store := newCheckpointTestStore(t, client)
	ctx := context.Background()

	// Save a pending checkpoint
	cp := &ExecutionCheckpoint{
		CheckpointID:   "cp-workflow",
		RequestID:      "req-workflow",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointPlanGenerated,
	}
	if err := store.SaveCheckpoint(ctx, cp); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	// List pending - should include our checkpoint
	pending, err := store.ListPendingCheckpoints(ctx, CheckpointFilter{})
	if err != nil {
		t.Fatalf("ListPendingCheckpoints() error = %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("Expected 1 pending checkpoint, got %d", len(pending))
	}

	// Update status to approved
	if err := store.UpdateCheckpointStatus(ctx, "cp-workflow", CheckpointStatusApproved); err != nil {
		t.Fatalf("UpdateCheckpointStatus() error = %v", err)
	}

	// Verify the checkpoint was updated
	loaded, err := store.LoadCheckpoint(ctx, "cp-workflow")
	if err != nil {
		t.Fatalf("LoadCheckpoint() error = %v", err)
	}
	if loaded.Status != CheckpointStatusApproved {
		t.Errorf("Status = %q, want %q", loaded.Status, CheckpointStatusApproved)
	}
}

// -----------------------------------------------------------------------------
// Logger Tests
// -----------------------------------------------------------------------------

func TestSaveCheckpoint_LogsOnDebug(t *testing.T) {
	mr, client := setupCheckpointTestRedis(t)
	defer mr.Close()
	defer func() { _ = client.Close() }()

	logger := &checkpointTestCapturingLogger{}
	store := newCheckpointTestStore(t, client)
	store.logger = logger

	ctx := context.Background()
	cp := &ExecutionCheckpoint{
		CheckpointID:   "cp-123",
		Status:         CheckpointStatusPending,
		InterruptPoint: InterruptPointPlanGenerated,
	}

	if err := store.SaveCheckpoint(ctx, cp); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}

	if len(logger.debugMessages) == 0 {
		t.Error("Expected debug message to be logged on successful save")
	}
}

// checkpointTestCapturingLogger captures log messages for testing
type checkpointTestCapturingLogger struct {
	core.NoOpLogger
	errorMessages []string
	warnMessages  []string
	infoMessages  []string
	debugMessages []string
}

func (l *checkpointTestCapturingLogger) ErrorWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	l.errorMessages = append(l.errorMessages, msg)
}

func (l *checkpointTestCapturingLogger) WarnWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	l.warnMessages = append(l.warnMessages, msg)
}

func (l *checkpointTestCapturingLogger) InfoWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	l.infoMessages = append(l.infoMessages, msg)
}

func (l *checkpointTestCapturingLogger) DebugWithContext(ctx context.Context, msg string, fields map[string]interface{}) {
	l.debugMessages = append(l.debugMessages, msg)
}

// =============================================================================
// Agent Name Fallback Tests (RC2)
// =============================================================================

// TestCheckpointStoreAgentNameFallback verifies TRUVAG3_AGENT_NAME > TRUVAG3_K8S_SERVICE_NAME
// precedence in NewRedisCheckpointStore key prefix, per CORE_DESIGN_PRINCIPLES.md.
func TestCheckpointStoreAgentNameFallback(t *testing.T) {
	testCases := []struct {
		name           string
		agentName      string // TRUVAG3_AGENT_NAME
		serviceName    string // TRUVAG3_K8S_SERVICE_NAME
		expectedPrefix string
	}{
		{
			name:           "agent name set",
			agentName:      "event-driven-agent",
			serviceName:    "",
			expectedPrefix: "truvag3:hitl:event-driven-agent",
		},
		{
			name:           "K8s fallback",
			agentName:      "",
			serviceName:    "event-driven-agent",
			expectedPrefix: "truvag3:hitl:event-driven-agent",
		},
		{
			name:           "neither set — bare prefix",
			agentName:      "",
			serviceName:    "",
			expectedPrefix: "truvag3:hitl",
		},
		{
			name:           "agent name wins over service name",
			agentName:      "agent",
			serviceName:    "service",
			expectedPrefix: "truvag3:hitl:agent",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Use t.Setenv for automatic cleanup
			t.Setenv("TRUVAG3_AGENT_NAME", tc.agentName)
			t.Setenv("TRUVAG3_K8S_SERVICE_NAME", tc.serviceName)

			mr, err := miniredis.Run()
			if err != nil {
				t.Fatalf("Failed to start miniredis: %v", err)
			}
			defer mr.Close()

			// Call the real constructor so the test exercises actual resolution logic.
			store, err := NewRedisCheckpointStore(
				WithCheckpointRedisURL("redis://"+mr.Addr()),
				WithInstanceID("test-instance"),
			)
			if err != nil {
				t.Fatalf("NewRedisCheckpointStore failed: %v", err)
			}
			defer func() { _ = store.Close() }()

			if store.keyPrefix != tc.expectedPrefix {
				t.Errorf("keyPrefix = %q, want %q", store.keyPrefix, tc.expectedPrefix)
			}

			// Verify SaveCheckpoint stamps agentName onto checkpoint
			expectedAgentName := tc.agentName
			if expectedAgentName == "" {
				expectedAgentName = tc.serviceName
			}

			cp := &ExecutionCheckpoint{
				CheckpointID: "cp-test-123",
				RequestID:    "req-456",
				Status:       CheckpointStatusPending,
				CreatedAt:    time.Now(),
				ExpiresAt:    time.Now().Add(1 * time.Hour),
				StepResults:  map[string]*StepResult{},
			}

			err = store.SaveCheckpoint(context.Background(), cp)
			if err != nil {
				t.Fatalf("SaveCheckpoint failed: %v", err)
			}

			if expectedAgentName != "" && cp.AgentName != expectedAgentName {
				t.Errorf("SaveCheckpoint did not stamp AgentName: got %q, want %q", cp.AgentName, expectedAgentName)
			}
		})
	}
}
