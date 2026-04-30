package orchestration

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/go-redis/redis/v8"
	"github.com/truvaagents/truva-g3/core"
)

// Tests for ORCH-022: HITL-interrupted execution records carry prior-phase steps.
//
// Covers:
//   - buildNonSuccessResult (Layer 1 helper)
//   - extractCurrentPhaseFromCheckpoint (Layer 1 helper)
//   - rebuildCheckpointCompletedSteps (Layer 2 helper)
//   - Execution-store TTL carve-out for interrupted records (all four branches)

// ---------------------------------------------------------------------------
// buildNonSuccessResult
// ---------------------------------------------------------------------------

func TestBuildNonSuccessResult_StepLevelInterrupt(t *testing.T) {
	t0 := time.Date(2026, 4, 24, 15, 53, 25, 0, time.UTC)
	prior := []StepResult{
		{StepID: "step-1", AgentName: "devops-tool", Success: true, StartTime: t0, EndTime: t0.Add(21 * time.Second), Metadata: map[string]interface{}{"phase_number": 1}},
		{StepID: "step-2", AgentName: "devops-tool", Success: true, StartTime: t0.Add(time.Second), EndTime: t0.Add(22 * time.Second), Metadata: map[string]interface{}{"phase_number": 1}},
	}
	current := []StepResult{
		{StepID: "step-4", AgentName: "jira-tool", Success: true, StartTime: t0.Add(45 * time.Second)},
	}
	plans := []*RoutingPlan{
		{PlanID: "plan-1", Steps: []RoutingStep{{StepID: "step-1"}, {StepID: "step-2"}}},
		{PlanID: "plan-1", Steps: []RoutingStep{{StepID: "step-3"}, {StepID: "step-4"}, {StepID: "step-5"}}},
	}

	r := buildNonSuccessResult(current, plans, 2, false, prior, "plan-1", false)

	if r == nil {
		t.Fatal("buildNonSuccessResult returned nil")
	}
	if len(r.Steps) != 3 {
		t.Fatalf("Steps length = %d, want 3", len(r.Steps))
	}
	want := []string{"step-1", "step-2", "step-4"}
	for i, s := range r.Steps {
		if s.StepID != want[i] {
			t.Errorf("Steps[%d].StepID = %q, want %q", i, s.StepID, want[i])
		}
	}
	// step-4 must be stamped with phase_number=2 by the helper
	if got := r.Steps[2].Metadata["phase_number"]; got != 2 {
		t.Errorf("Steps[2].Metadata[phase_number] = %v, want 2", got)
	}
	// Existing phase_number on prior steps must not be overwritten
	if got := r.Steps[0].Metadata["phase_number"]; got != 1 {
		t.Errorf("Steps[0].Metadata[phase_number] = %v, want 1 (not overwritten)", got)
	}
	if r.PhaseCount != 2 {
		t.Errorf("PhaseCount = %d, want 2", r.PhaseCount)
	}
	if r.Success {
		t.Error("Success = true, want false for interrupt site")
	}
	// Metadata round-trips for storeExecutionAsync extraction
	if gotPlans, ok := r.Metadata[MetadataKeyPhasePlans].([]*RoutingPlan); !ok || len(gotPlans) != 2 {
		t.Errorf("Metadata[PhasePlans] type/length wrong: %v", r.Metadata[MetadataKeyPhasePlans])
	}
	if got := r.Metadata[MetadataKeyPhaseCount]; got != 2 {
		t.Errorf("Metadata[PhaseCount] = %v, want 2", got)
	}
	if got := r.Metadata[MetadataKeyForcedTerminal]; got != false {
		t.Errorf("Metadata[ForcedTerminal] = %v, want false", got)
	}
}

func TestBuildNonSuccessResult_PlanLevelInterrupt_NoCurrentPhase(t *testing.T) {
	t0 := time.Now()
	prior := []StepResult{
		{StepID: "step-1", Success: true, StartTime: t0},
		{StepID: "step-2", Success: true, StartTime: t0.Add(time.Second)},
	}
	plans := []*RoutingPlan{
		{PlanID: "plan-1", Steps: []RoutingStep{{StepID: "step-1"}, {StepID: "step-2"}}},
	}

	r := buildNonSuccessResult(nil, plans, 1, false, prior, "plan-1", false)

	if len(r.Steps) != 2 {
		t.Fatalf("Steps length = %d, want 2 (allStepsList only)", len(r.Steps))
	}
	if r.Metadata[MetadataKeyPhasePlans] == nil {
		t.Error("PhasePlans metadata missing")
	}
}

func TestBuildNonSuccessResult_IntermediateStoreSuccessFlag(t *testing.T) {
	r := buildNonSuccessResult(nil, nil, 1, false, nil, "p", true)
	if !r.Success {
		t.Error("Success = false, want true for intermediate-store site")
	}
	r2 := buildNonSuccessResult(nil, nil, 1, false, nil, "p", false)
	if r2.Success {
		t.Error("Success = true, want false for interrupt/error sites")
	}
}

func TestBuildNonSuccessResult_PhaseNumberPreservesExisting(t *testing.T) {
	current := []StepResult{
		{StepID: "step-99", Metadata: map[string]interface{}{"phase_number": 99}},
	}
	r := buildNonSuccessResult(current, nil, 2, false, nil, "p", false)
	if got := r.Steps[0].Metadata["phase_number"]; got != 99 {
		t.Errorf("phase_number = %v, want 99 (not overwritten)", got)
	}
}

// TestBuildNonSuccessResult_DoesNotMutateCallerMetadata guards the
// deep-copy-on-write invariant. buildNonSuccessResult receives StepResult
// values whose Metadata maps may be aliased with the caller's state (most
// notably with checkpoint.StepResults[*].Metadata at the step-level HITL
// interrupt site). The helper MUST NOT mutate those shared maps when it
// stamps phase_number — it allocates a fresh map instead.
func TestBuildNonSuccessResult_DoesNotMutateCallerMetadata(t *testing.T) {
	// Caller's map — shared across both currentPhaseSteps entries below and
	// held separately by the caller. This simulates the real aliasing at site
	// 2259 where extractCurrentPhaseFromCheckpoint returns entries whose
	// Metadata pointers still reference the checkpoint's map.
	sharedMeta := map[string]interface{}{"existing_key": "existing_value"}
	current := []StepResult{
		{StepID: "step-shared-a", Metadata: sharedMeta},
		{StepID: "step-shared-b", Metadata: sharedMeta},
	}

	_ = buildNonSuccessResult(current, nil, 7, false, nil, "p", false)

	// The caller's map must not have been mutated.
	if _, stamped := sharedMeta["phase_number"]; stamped {
		t.Errorf("helper mutated caller's Metadata map: %v", sharedMeta)
	}
	if got := sharedMeta["existing_key"]; got != "existing_value" {
		t.Errorf("caller's existing key corrupted: %v", got)
	}
	if len(sharedMeta) != 1 {
		t.Errorf("caller's map size changed: %d keys, want 1", len(sharedMeta))
	}
}

func TestBuildNonSuccessResult_PhasePlansDefensiveCopy(t *testing.T) {
	plans := []*RoutingPlan{
		{PlanID: "p1"},
		{PlanID: "p2"},
	}
	r := buildNonSuccessResult(nil, plans, 2, false, nil, "p1", false)

	// Extract the stored slice and mutate the original caller's slice.
	stored := r.Metadata[MetadataKeyPhasePlans].([]*RoutingPlan)
	plans = append(plans, &RoutingPlan{PlanID: "p3"})

	if len(plans) != 3 {
		t.Fatalf("caller-side plans length after append = %d, want 3 (sanity check)", len(plans))
	}
	if len(stored) != 2 {
		t.Errorf("stored phasePlans length = %d, want 2 (caller append should not leak)", len(stored))
	}
}

func TestBuildNonSuccessResult_ForcedTerminal(t *testing.T) {
	r := buildNonSuccessResult(nil, nil, 3, true, nil, "p", false)
	if got := r.Metadata[MetadataKeyForcedTerminal]; got != true {
		t.Errorf("Metadata[ForcedTerminal] = %v, want true", got)
	}
}

// ---------------------------------------------------------------------------
// extractCurrentPhaseFromCheckpoint
// ---------------------------------------------------------------------------

func TestExtractCurrentPhaseFromCheckpoint_FiltersPriorPhases(t *testing.T) {
	t0 := time.Now()
	cp := &ExecutionCheckpoint{
		StepResults: map[string]*StepResult{
			"step-1": {StepID: "step-1", StartTime: t0},
			"step-2": {StepID: "step-2", StartTime: t0.Add(time.Second)},
			"step-4": {StepID: "step-4", StartTime: t0.Add(10 * time.Second)},
		},
	}
	prior := map[string]*StepResult{
		"step-1": {StepID: "step-1"},
		"step-2": {StepID: "step-2"},
	}

	out := extractCurrentPhaseFromCheckpoint(cp, prior)

	if len(out) != 1 {
		t.Fatalf("output length = %d, want 1 (only step-4)", len(out))
	}
	if out[0].StepID != "step-4" {
		t.Errorf("output[0].StepID = %q, want step-4", out[0].StepID)
	}
}

func TestExtractCurrentPhaseFromCheckpoint_SortsByStartTime(t *testing.T) {
	t0 := time.Now()
	cp := &ExecutionCheckpoint{
		StepResults: map[string]*StepResult{
			"b": {StepID: "b", StartTime: t0.Add(2 * time.Second)},
			"a": {StepID: "a", StartTime: t0},
			"c": {StepID: "c", StartTime: t0.Add(4 * time.Second)},
		},
	}
	out := extractCurrentPhaseFromCheckpoint(cp, nil)

	if len(out) != 3 {
		t.Fatalf("output length = %d, want 3", len(out))
	}
	want := []string{"a", "b", "c"}
	for i, s := range out {
		if s.StepID != want[i] {
			t.Errorf("output[%d].StepID = %q, want %q", i, s.StepID, want[i])
		}
	}
}

func TestExtractCurrentPhaseFromCheckpoint_NilSafety(t *testing.T) {
	if out := extractCurrentPhaseFromCheckpoint(nil, nil); out != nil {
		t.Errorf("nil checkpoint: got %v, want nil", out)
	}
	cp := &ExecutionCheckpoint{StepResults: nil}
	if out := extractCurrentPhaseFromCheckpoint(cp, nil); out != nil {
		t.Errorf("nil StepResults: got %v, want nil", out)
	}
	cp2 := &ExecutionCheckpoint{StepResults: map[string]*StepResult{}}
	if out := extractCurrentPhaseFromCheckpoint(cp2, nil); out != nil {
		t.Errorf("empty StepResults: got %v, want nil", out)
	}
}

// ---------------------------------------------------------------------------
// rebuildCheckpointCompletedSteps
// ---------------------------------------------------------------------------

func TestRebuildCheckpointCompletedSteps_CrossPhase(t *testing.T) {
	t0 := time.Now()
	cp := &ExecutionCheckpoint{
		StepResults: map[string]*StepResult{
			"step-1": {StepID: "step-1", Success: true, StartTime: t0},
			"step-2": {StepID: "step-2", Success: true, StartTime: t0.Add(time.Second)},
			"step-4": {StepID: "step-4", Success: true, StartTime: t0.Add(10 * time.Second)},
		},
	}

	rebuildCheckpointCompletedSteps(cp)

	if len(cp.CompletedSteps) != 3 {
		t.Fatalf("CompletedSteps length = %d, want 3", len(cp.CompletedSteps))
	}
	want := []string{"step-1", "step-2", "step-4"}
	for i, s := range cp.CompletedSteps {
		if s.StepID != want[i] {
			t.Errorf("CompletedSteps[%d].StepID = %q, want %q", i, s.StepID, want[i])
		}
	}
}

func TestRebuildCheckpointCompletedSteps_FiltersFailures(t *testing.T) {
	t0 := time.Now()
	cp := &ExecutionCheckpoint{
		StepResults: map[string]*StepResult{
			"step-1": {StepID: "step-1", Success: true, StartTime: t0},
			"step-2": {StepID: "step-2", Success: false, StartTime: t0.Add(time.Second)},
		},
	}
	rebuildCheckpointCompletedSteps(cp)
	if len(cp.CompletedSteps) != 1 || cp.CompletedSteps[0].StepID != "step-1" {
		t.Errorf("CompletedSteps = %+v, want only step-1", cp.CompletedSteps)
	}
}

func TestRebuildCheckpointCompletedSteps_NilOrEmpty(t *testing.T) {
	rebuildCheckpointCompletedSteps(nil) // must not panic

	cp := &ExecutionCheckpoint{StepResults: nil, CompletedSteps: []StepResult{{StepID: "prior"}}}
	rebuildCheckpointCompletedSteps(cp)
	// Empty/nil StepResults is a no-op — existing CompletedSteps stays untouched.
	if len(cp.CompletedSteps) != 1 || cp.CompletedSteps[0].StepID != "prior" {
		t.Errorf("no-op on nil StepResults violated: %+v", cp.CompletedSteps)
	}
}

// ---------------------------------------------------------------------------
// TTL carve-out (Store path, in-memory store via mockStorageProvider)
// ---------------------------------------------------------------------------

// ttlCapturingProvider wraps mockStorageProvider and records the TTL for each
// Set call so tests can assert on it.
type ttlCapturingProvider struct {
	mockStorageProvider
	ttlsMu sync.Mutex
	ttls   map[string]time.Duration
}

func newTTLCapturingProvider() *ttlCapturingProvider {
	p := &ttlCapturingProvider{
		mockStorageProvider: mockStorageProvider{
			data:    make(map[string]string),
			indexes: make(map[string]map[string]float64),
		},
		ttls: make(map[string]time.Duration),
	}
	return p
}

func (p *ttlCapturingProvider) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	p.ttlsMu.Lock()
	p.ttls[key] = ttl
	p.ttlsMu.Unlock()
	return p.mockStorageProvider.Set(ctx, key, value, ttl)
}

func (p *ttlCapturingProvider) lastTTL(key string) time.Duration {
	p.ttlsMu.Lock()
	defer p.ttlsMu.Unlock()
	return p.ttls[key]
}

func ttlStoreFixture(t *testing.T) (*ttlCapturingProvider, ExecutionStore) {
	t.Helper()
	provider := newTTLCapturingProvider()
	config := DefaultExecutionStoreConfig()
	config.TTL = 24 * time.Hour
	config.ErrorTTL = 1 * time.Hour // Intentionally shorter than TTL so carve-out is observable
	store := NewExecutionStoreWithProvider(provider, config, nil)
	return provider, store
}

func TestStore_InterruptedUsesDefaultTTL(t *testing.T) {
	provider, store := ttlStoreFixture(t)
	ctx := context.Background()

	exec := &StoredExecution{
		RequestID:       "req-interrupt",
		OriginalRequest: "test",
		CreatedAt:       time.Now(),
		Interrupted:     true,
		Result:          &ExecutionResult{PlanID: "p1", Success: false}, // Post-fix shape
	}
	if err := store.Store(ctx, exec); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	got := provider.lastTTL("truvag3:execution:debug::req-interrupt")
	if got != 24*time.Hour {
		t.Errorf("interrupted TTL = %v, want 24h (default); got errorTTL-routing regression", got)
	}
}

func TestStore_ErrorUsesErrorTTL(t *testing.T) {
	provider, store := ttlStoreFixture(t)
	ctx := context.Background()

	exec := &StoredExecution{
		RequestID:       "req-error",
		OriginalRequest: "test",
		CreatedAt:       time.Now(),
		Interrupted:     false,
		Result:          &ExecutionResult{PlanID: "p1", Success: false},
	}
	if err := store.Store(ctx, exec); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	got := provider.lastTTL("truvag3:execution:debug::req-error")
	if got != 1*time.Hour {
		t.Errorf("errored TTL = %v, want 1h (errorTTL)", got)
	}
}

func TestStore_SuccessUsesDefaultTTL(t *testing.T) {
	provider, store := ttlStoreFixture(t)
	ctx := context.Background()

	exec := &StoredExecution{
		RequestID:       "req-success",
		OriginalRequest: "test",
		CreatedAt:       time.Now(),
		Result:          &ExecutionResult{PlanID: "p1", Success: true},
	}
	if err := store.Store(ctx, exec); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	got := provider.lastTTL("truvag3:execution:debug::req-success")
	if got != 24*time.Hour {
		t.Errorf("success TTL = %v, want 24h", got)
	}
}

// SetMetadata exercises the second TTL branch in the in-memory store
// (execution_store.go:406-409). The Redis store's corresponding Update method
// has the same branch (redis_execution_store.go:336-339), covered by the same
// fix; Redis-specific tests are out of scope for this unit-test fixture.

func TestSetMetadata_InterruptedUsesDefaultTTL(t *testing.T) {
	provider, store := ttlStoreFixture(t)
	ctx := context.Background()

	exec := &StoredExecution{
		RequestID:       "req-sm-interrupt",
		OriginalRequest: "test",
		CreatedAt:       time.Now(),
		Interrupted:     true,
		Result:          &ExecutionResult{PlanID: "p1", Success: false},
	}
	if err := store.Store(ctx, exec); err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if err := store.SetMetadata(ctx, "req-sm-interrupt", "note", "approved"); err != nil {
		t.Fatalf("SetMetadata failed: %v", err)
	}
	got := provider.lastTTL("truvag3:execution:debug::req-sm-interrupt")
	if got != 24*time.Hour {
		t.Errorf("interrupted SetMetadata TTL = %v, want 24h", got)
	}
}

func TestSetMetadata_ErrorUsesErrorTTL(t *testing.T) {
	provider, store := ttlStoreFixture(t)
	ctx := context.Background()

	exec := &StoredExecution{
		RequestID:       "req-sm-error",
		OriginalRequest: "test",
		CreatedAt:       time.Now(),
		Interrupted:     false,
		Result:          &ExecutionResult{PlanID: "p1", Success: false},
	}
	if err := store.Store(ctx, exec); err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	if err := store.SetMetadata(ctx, "req-sm-error", "note", "probed"); err != nil {
		t.Fatalf("SetMetadata failed: %v", err)
	}
	got := provider.lastTTL("truvag3:execution:debug::req-sm-error")
	if got != 1*time.Hour {
		t.Errorf("errored SetMetadata TTL = %v, want 1h (errorTTL)", got)
	}
}

// ---------------------------------------------------------------------------
// TTL carve-out (Redis store, Store + Update paths) — uses miniredis so the
// tests run without a live Redis. Mirrors the pattern in
// redis_llm_debug_store_test.go.
// ---------------------------------------------------------------------------

func redisTTLStoreFixture(t *testing.T) (*miniredis.Miniredis, *RedisExecutionDebugStore) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := &RedisExecutionDebugStore{
		client:    client,
		logger:    &core.NoOpLogger{},
		keyPrefix: executionDebugKeyPrefix, // "truvag3:execution:debug:" (trailing colon required — recordKey just concatenates)
		ttl:       24 * time.Hour,
		errorTTL:  1 * time.Hour, // shorter than ttl so carve-out is observable
	}
	return mr, store
}

func TestRedisStore_InterruptedUsesDefaultTTL(t *testing.T) {
	mr, store := redisTTLStoreFixture(t)
	ctx := context.Background()

	exec := &StoredExecution{
		RequestID:       "req-redis-interrupt",
		OriginalRequest: "test",
		CreatedAt:       time.Now(),
		Interrupted:     true,
		Result:          &ExecutionResult{PlanID: "p1", Success: false},
	}
	if err := store.Store(ctx, exec); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	key := "truvag3:execution:debug:req-redis-interrupt"
	got := mr.TTL(key)
	if got != 24*time.Hour {
		t.Errorf("interrupted Redis Store TTL = %v, want 24h; carve-out regression", got)
	}
}

func TestRedisStore_ErrorUsesErrorTTL(t *testing.T) {
	mr, store := redisTTLStoreFixture(t)
	ctx := context.Background()

	exec := &StoredExecution{
		RequestID:       "req-redis-error",
		OriginalRequest: "test",
		CreatedAt:       time.Now(),
		Interrupted:     false,
		Result:          &ExecutionResult{PlanID: "p1", Success: false},
	}
	if err := store.Store(ctx, exec); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	key := "truvag3:execution:debug:req-redis-error"
	got := mr.TTL(key)
	if got != 1*time.Hour {
		t.Errorf("errored Redis Store TTL = %v, want 1h (errorTTL)", got)
	}
}

func TestRedisStore_SuccessUsesDefaultTTL(t *testing.T) {
	mr, store := redisTTLStoreFixture(t)
	ctx := context.Background()

	exec := &StoredExecution{
		RequestID:       "req-redis-success",
		OriginalRequest: "test",
		CreatedAt:       time.Now(),
		Result:          &ExecutionResult{PlanID: "p1", Success: true},
	}
	if err := store.Store(ctx, exec); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	key := "truvag3:execution:debug:req-redis-success"
	got := mr.TTL(key)
	if got != 24*time.Hour {
		t.Errorf("success Redis Store TTL = %v, want 24h", got)
	}
}

func TestRedisUpdate_InterruptedUsesDefaultTTL(t *testing.T) {
	mr, store := redisTTLStoreFixture(t)
	ctx := context.Background()

	exec := &StoredExecution{
		RequestID:       "req-redis-u-interrupt",
		OriginalRequest: "test",
		CreatedAt:       time.Now(),
		Interrupted:     true,
		Result:          &ExecutionResult{PlanID: "p1", Success: false},
	}
	if err := store.Store(ctx, exec); err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	// Fast-forward to make TTL deltas observable; Update uses s.ttl (full 24h)
	// so the post-update TTL should reset to full 24h even if initial TTL elapsed.
	mr.FastForward(time.Hour)

	exec.OriginalRequest = "updated"
	if err := store.Update(ctx, "req-redis-u-interrupt", exec); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	key := "truvag3:execution:debug:req-redis-u-interrupt"
	got := mr.TTL(key)
	if got != 24*time.Hour {
		t.Errorf("interrupted Redis Update TTL = %v, want 24h; carve-out regression", got)
	}
}

func TestRedisUpdate_ErrorUsesErrorTTL(t *testing.T) {
	mr, store := redisTTLStoreFixture(t)
	ctx := context.Background()

	exec := &StoredExecution{
		RequestID:       "req-redis-u-error",
		OriginalRequest: "test",
		CreatedAt:       time.Now(),
		Interrupted:     false,
		Result:          &ExecutionResult{PlanID: "p1", Success: false},
	}
	if err := store.Store(ctx, exec); err != nil {
		t.Fatalf("Store failed: %v", err)
	}
	mr.FastForward(time.Minute)

	if err := store.Update(ctx, "req-redis-u-error", exec); err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	key := "truvag3:execution:debug:req-redis-u-error"
	got := mr.TTL(key)
	if got != 1*time.Hour {
		t.Errorf("errored Redis Update TTL = %v, want 1h (errorTTL)", got)
	}
}

// ---------------------------------------------------------------------------
// storeExecutionAsync contract — end-to-end through the method with a
// captured mock store. Covers the PhaseCount struct-field fallback (the
// only new branch in the extraction code) plus sanity checks on the
// Metadata → typed-field round-trip.
// ---------------------------------------------------------------------------

// storeExecutionAsyncFixture builds a minimal AIOrchestrator wired to a
// captureExecStore (defined in iterative_planning_test.go — same package).
// Same-package test access lets us reach the unexported method and the
// WaitGroup for deterministic await on the async store goroutine.
func storeExecutionAsyncFixture(t *testing.T) (*AIOrchestrator, *captureExecStore) {
	t.Helper()
	cap := &captureExecStore{}
	o := &AIOrchestrator{
		config:         DefaultConfig(),
		executionStore: cap,
	}
	return o, cap
}

func lastStored(c *captureExecStore) *StoredExecution {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.stored) == 0 {
		return nil
	}
	return c.stored[len(c.stored)-1]
}

func TestStoreExecutionAsync_PhaseCountStructFieldFallback(t *testing.T) {
	// result has PhaseCount set on the struct but NOT in Metadata. The
	// fallback at storeExecutionAsync should copy from the struct field.
	o, cap := storeExecutionAsyncFixture(t)
	result := &ExecutionResult{
		PlanID:     "p1",
		PhaseCount: 3,
		// Metadata intentionally absent / nil
	}
	o.storeExecutionAsync(context.Background(), "req", "req-phasecount-fallback", nil, result, nil)
	o.executionWg.Wait()

	stored := lastStored(cap)
	if stored == nil {
		t.Fatal("store.Store was never invoked")
	}
	if stored.PhaseCount != 3 {
		t.Errorf("stored.PhaseCount = %d, want 3 (struct-field fallback)", stored.PhaseCount)
	}
}

func TestStoreExecutionAsync_MetadataWinsOverStructField(t *testing.T) {
	// Both struct field and Metadata are set. Metadata takes precedence.
	o, cap := storeExecutionAsyncFixture(t)
	result := &ExecutionResult{
		PlanID:     "p1",
		PhaseCount: 99,
		Metadata: map[string]interface{}{
			MetadataKeyPhaseCount: 2,
		},
	}
	o.storeExecutionAsync(context.Background(), "req", "req-metadata-precedence", nil, result, nil)
	o.executionWg.Wait()

	stored := lastStored(cap)
	if stored == nil {
		t.Fatal("store.Store was never invoked")
	}
	if stored.PhaseCount != 2 {
		t.Errorf("stored.PhaseCount = %d, want 2 (Metadata precedence over struct field)", stored.PhaseCount)
	}
}

func TestStoreExecutionAsync_PhasePlansRoundTrip(t *testing.T) {
	// Verify buildNonSuccessResult's Metadata round-trips through
	// storeExecutionAsync into stored.PhasePlans. This is the end-to-end
	// proof that the two halves of the fix (helper + extraction) cohere.
	o, cap := storeExecutionAsyncFixture(t)
	phasePlans := []*RoutingPlan{
		{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-1"}}},
		{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-2"}}},
	}
	result := buildNonSuccessResult(nil, phasePlans, 2, false, nil, "p1", false)

	o.storeExecutionAsync(context.Background(), "req", "req-roundtrip", nil, result, nil)
	o.executionWg.Wait()

	stored := lastStored(cap)
	if stored == nil {
		t.Fatal("store.Store was never invoked")
	}
	if len(stored.PhasePlans) != 2 {
		t.Errorf("stored.PhasePlans length = %d, want 2", len(stored.PhasePlans))
	}
	if stored.PhaseCount != 2 {
		t.Errorf("stored.PhaseCount = %d, want 2", stored.PhaseCount)
	}
}

func TestStoreExecutionAsync_InterruptedFlag(t *testing.T) {
	// checkpoint != nil sets Interrupted = true on the stored record.
	// This is the canonical "is interrupted" signal.
	o, cap := storeExecutionAsyncFixture(t)
	cp := &ExecutionCheckpoint{CheckpointID: "cp-x"}
	result := buildNonSuccessResult(nil, nil, 1, false, nil, "p1", false)

	o.storeExecutionAsync(context.Background(), "req", "req-interrupted-flag", nil, result, cp)
	o.executionWg.Wait()

	stored := lastStored(cap)
	if stored == nil {
		t.Fatal("store.Store was never invoked")
	}
	if !stored.Interrupted {
		t.Error("stored.Interrupted = false, want true when checkpoint != nil")
	}
	if stored.Checkpoint == nil || stored.Checkpoint.CheckpointID != "cp-x" {
		t.Errorf("stored.Checkpoint = %v, want checkpoint with id cp-x", stored.Checkpoint)
	}
}

// ---------------------------------------------------------------------------
// Call-site wiring tests — one per site in executePhaseLoop. These tests
// reproduce each call site's argument shape and drive it through
// storeExecutionAsync, asserting the stored record matches the contract.
// The executePhaseLoop body itself is not exercised (requires mocking
// planner/executor/interruptController — per-package convention, the phase
// loop is not tested end-to-end in unit tests). Instead, each test asserts
// "if the call site passes these arguments, the stored record is correct."
// Together with the helper unit tests, this covers every code path added
// by ORCH-022 at the orchestrator.go level except the in-loop plumbing
// itself (which is a thin dispatcher over already-tested helpers).
// ---------------------------------------------------------------------------

// TestCallSite_PlanLevelHITL matches orchestrator.go line ~2336:
//
//	buildNonSuccessResult(nil, phasePlans, phaseCount, forcedTerminal, allStepsList, plan.PlanID, false)
//
// — plan-level HITL interrupt: no execution in this phase yet, currentPhaseSteps = nil,
// Success = false, checkpoint != nil.
func TestCallSite_PlanLevelHITL(t *testing.T) {
	o, cap := storeExecutionAsyncFixture(t)
	t0 := time.Now()
	allStepsList := []StepResult{
		{StepID: "step-1", Success: true, StartTime: t0, Metadata: map[string]interface{}{"phase_number": 1}},
	}
	phasePlans := []*RoutingPlan{
		{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-1"}}},
		{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-2"}}},
	}
	cp := &ExecutionCheckpoint{CheckpointID: "cp-plan", InterruptPoint: "before_plan"}

	result := buildNonSuccessResult(nil, phasePlans, 2, false, allStepsList, "p1", false)
	o.storeExecutionAsync(context.Background(), "req", "req-plan-hitl", nil, result, cp)
	o.executionWg.Wait()

	stored := lastStored(cap)
	if stored == nil {
		t.Fatal("store.Store was never invoked")
	}
	if !stored.Interrupted {
		t.Error("Interrupted = false, want true for plan-level HITL")
	}
	if len(stored.PhasePlans) != 2 {
		t.Errorf("PhasePlans length = %d, want 2", len(stored.PhasePlans))
	}
	if stored.PhaseCount != 2 {
		t.Errorf("PhaseCount = %d, want 2", stored.PhaseCount)
	}
	if stored.Result == nil || len(stored.Result.Steps) != 1 || stored.Result.Steps[0].StepID != "step-1" {
		t.Errorf("Result.Steps should contain only prior-phase steps, got %+v", stored.Result)
	}
	if stored.Result.Success {
		t.Error("Result.Success = true, want false for interrupted record")
	}
}

// TestCallSite_StepLevelHITL matches orchestrator.go line ~2427:
//
//	currentPhaseSteps := extractCurrentPhaseFromCheckpoint(checkpoint, allStepResults)
//	rebuildCheckpointCompletedSteps(checkpoint)
//	buildNonSuccessResult(currentPhaseSteps, phasePlans, phaseCount, forcedTerminal, allStepsList, plan.PlanID, false)
//
// — step-level HITL interrupt: executor returned (nil, ErrInterrupted), checkpoint carries
// current-phase siblings + prior-phase enrichment. After rebuild, CompletedSteps is
// cross-phase. Result.Steps must contain prior phases + current-phase siblings
// (but NOT the interrupted step itself).
func TestCallSite_StepLevelHITL(t *testing.T) {
	o, cap := storeExecutionAsyncFixture(t)
	t0 := time.Now()

	// Prior phase: step-1 and step-2 completed in Phase 1.
	allStepResults := map[string]*StepResult{
		"step-1": {StepID: "step-1", Success: true, StartTime: t0, Metadata: map[string]interface{}{"phase_number": 1}},
		"step-2": {StepID: "step-2", Success: true, StartTime: t0.Add(time.Second), Metadata: map[string]interface{}{"phase_number": 1}},
	}
	allStepsList := []StepResult{
		*allStepResults["step-1"],
		*allStepResults["step-2"],
	}
	phasePlans := []*RoutingPlan{
		{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-1"}, {StepID: "step-2"}}},
		{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-3"}, {StepID: "step-4"}, {StepID: "step-5"}}},
	}

	// Checkpoint simulates executor.go:836 populating StepResults with
	// current-phase completed sibling (step-4). The orchestrator enrichment
	// block folds in prior phases (step-1, step-2). CurrentStep points at
	// the interrupted step (step-3).
	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp-step",
		CurrentStep:  &RoutingStep{StepID: "step-3"},
		StepResults: map[string]*StepResult{
			"step-1": allStepResults["step-1"],
			"step-2": allStepResults["step-2"],
			"step-4": {StepID: "step-4", Success: true, StartTime: t0.Add(45 * time.Second)},
		},
	}

	// Wiring: exactly what the call site does.
	currentPhaseSteps := extractCurrentPhaseFromCheckpoint(checkpoint, allStepResults)
	rebuildCheckpointCompletedSteps(checkpoint)
	result := buildNonSuccessResult(currentPhaseSteps, phasePlans, 2, false, allStepsList, "p1", false)
	o.storeExecutionAsync(context.Background(), "req", "req-step-hitl", nil, result, checkpoint)
	o.executionWg.Wait()

	stored := lastStored(cap)
	if stored == nil {
		t.Fatal("store.Store was never invoked")
	}
	if !stored.Interrupted {
		t.Error("Interrupted = false, want true")
	}
	if stored.PhaseCount != 2 || len(stored.PhasePlans) != 2 {
		t.Errorf("phase metadata = (%d, %d), want (2, 2)", stored.PhaseCount, len(stored.PhasePlans))
	}
	// Result.Steps must be [step-1, step-2, step-4] — prior phases + current-phase sibling,
	// WITHOUT the interrupted step-3.
	if stored.Result == nil {
		t.Fatal("Result nil")
	}
	gotIDs := make([]string, 0, len(stored.Result.Steps))
	for _, s := range stored.Result.Steps {
		gotIDs = append(gotIDs, s.StepID)
	}
	want := []string{"step-1", "step-2", "step-4"}
	if len(gotIDs) != len(want) {
		t.Fatalf("Result.Steps IDs = %v, want %v", gotIDs, want)
	}
	for i, id := range want {
		if gotIDs[i] != id {
			t.Errorf("Result.Steps[%d] = %q, want %q", i, gotIDs[i], id)
		}
	}
	// Checkpoint.CompletedSteps must be cross-phase after rebuild (Layer 2).
	if stored.Checkpoint == nil {
		t.Fatal("Checkpoint nil")
	}
	if len(stored.Checkpoint.CompletedSteps) != 3 {
		t.Errorf("Checkpoint.CompletedSteps length = %d, want 3 (cross-phase after rebuild)", len(stored.Checkpoint.CompletedSteps))
	}
}

// TestCallSite_PhaseExecutionError matches orchestrator.go line ~2465:
//
//	var errorCurrentPhaseSteps []StepResult
//	if phaseResult != nil { errorCurrentPhaseSteps = phaseResult.Steps }
//	buildNonSuccessResult(errorCurrentPhaseSteps, phasePlans, phaseCount, forcedTerminal, allStepsList, plan.PlanID, false)
//
// — phase execution error (non-interrupt): phaseResult may carry partial steps,
// checkpoint is nil. Success = false.
func TestCallSite_PhaseExecutionError(t *testing.T) {
	o, cap := storeExecutionAsyncFixture(t)
	t0 := time.Now()
	allStepsList := []StepResult{
		{StepID: "step-1", Success: true, StartTime: t0},
	}
	phasePlans := []*RoutingPlan{
		{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-1"}}},
		{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-2"}, {StepID: "step-3"}}},
	}
	// phaseResult carries one succeeded step and one that errored partially.
	// This test covers the non-nil branch of the call site's
	// `if phaseResult != nil` guard; the nil branch is in
	// TestCallSite_PhaseExecutionError_NilPhaseResult.
	phaseResult := &ExecutionResult{
		Steps: []StepResult{
			{StepID: "step-2", Success: true, StartTime: t0.Add(10 * time.Second)},
			{StepID: "step-3", Success: false, Error: "boom", StartTime: t0.Add(11 * time.Second)},
		},
	}
	result := buildNonSuccessResult(phaseResult.Steps, phasePlans, 2, false, allStepsList, "p1", false)
	o.storeExecutionAsync(context.Background(), "req", "req-exec-error", nil, result, nil)
	o.executionWg.Wait()

	stored := lastStored(cap)
	if stored == nil {
		t.Fatal("store.Store was never invoked")
	}
	if stored.Interrupted {
		t.Error("Interrupted = true, want false for execution-error site")
	}
	if stored.Checkpoint != nil {
		t.Error("Checkpoint should be nil for execution-error site")
	}
	if len(stored.PhasePlans) != 2 {
		t.Errorf("PhasePlans length = %d, want 2", len(stored.PhasePlans))
	}
	// Result.Steps must include prior step-1 AND current-phase steps (step-2 succ, step-3 fail).
	if stored.Result == nil || len(stored.Result.Steps) != 3 {
		t.Fatalf("Result.Steps length = %d, want 3", len(stored.Result.Steps))
	}
	// Success flag is false regardless of individual step success.
	if stored.Result.Success {
		t.Error("Result.Success = true, want false for error site")
	}
}

// TestCallSite_PhaseExecutionError_NilPhaseResult covers the nil-guard at
// line ~2461-2464 — phaseResult may be nil when the executor crashes
// before producing any steps. currentPhaseSteps defaults to nil.
func TestCallSite_PhaseExecutionError_NilPhaseResult(t *testing.T) {
	o, cap := storeExecutionAsyncFixture(t)
	allStepsList := []StepResult{{StepID: "step-1", Success: true}}
	phasePlans := []*RoutingPlan{{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-1"}}}}

	var phaseResult *ExecutionResult // nil
	var errorCurrentPhaseSteps []StepResult
	if phaseResult != nil {
		errorCurrentPhaseSteps = phaseResult.Steps
	}
	result := buildNonSuccessResult(errorCurrentPhaseSteps, phasePlans, 1, false, allStepsList, "p1", false)
	o.storeExecutionAsync(context.Background(), "req", "req-exec-error-nil", nil, result, nil)
	o.executionWg.Wait()

	stored := lastStored(cap)
	if stored == nil {
		t.Fatal("store.Store was never invoked")
	}
	// Result.Steps = allStepsList only; no panic on nil phaseResult.
	if stored.Result == nil || len(stored.Result.Steps) != 1 {
		t.Errorf("Result.Steps length = %d, want 1 (allStepsList only)", len(stored.Result.Steps))
	}
}

// TestCallSite_IntermediateStore matches orchestrator.go line ~2565:
//
//	buildNonSuccessResult(nil, phasePlans, phaseCount, forcedTerminal, allStepsList, plan.PlanID, true)
//
// — inter-phase intermediate store: currentPhaseSteps = nil (current phase already
// accumulated into allStepsList), success = TRUE, checkpoint = nil.
func TestCallSite_IntermediateStore(t *testing.T) {
	o, cap := storeExecutionAsyncFixture(t)
	t0 := time.Now()
	// allStepsList after Phase 1 accumulator: step-1 already appended with phase_number=1.
	allStepsList := []StepResult{
		{StepID: "step-1", Success: true, StartTime: t0, Metadata: map[string]interface{}{"phase_number": 1}},
		{StepID: "step-2", Success: true, StartTime: t0.Add(time.Second), Metadata: map[string]interface{}{"phase_number": 1}},
	}
	phasePlans := []*RoutingPlan{
		{PlanID: "p1", Steps: []RoutingStep{{StepID: "step-1"}, {StepID: "step-2"}}},
	}

	result := buildNonSuccessResult(nil, phasePlans, 1, false, allStepsList, "p1", true)
	o.storeExecutionAsync(context.Background(), "req", "req-intermediate", nil, result, nil)
	o.executionWg.Wait()

	stored := lastStored(cap)
	if stored == nil {
		t.Fatal("store.Store was never invoked")
	}
	if stored.Interrupted {
		t.Error("Interrupted = true, want false for intermediate site")
	}
	if !stored.Result.Success {
		t.Error("Result.Success = false, want true for intermediate site")
	}
	if len(stored.Result.Steps) != 2 {
		t.Errorf("Result.Steps length = %d, want 2 (allStepsList after Phase 1)", len(stored.Result.Steps))
	}
	if stored.PhaseCount != 1 {
		t.Errorf("PhaseCount = %d, want 1", stored.PhaseCount)
	}
}
