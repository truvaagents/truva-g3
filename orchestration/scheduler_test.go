// Package orchestration — unit tests for the Scheduler component.
//
// These tests exercise the Scheduler tick loop, fire logic, and helpers in
// isolation using mock implementations of the core interfaces. Integration
// with real Redis / worker pools is covered by Phase 5 integration tests.

package orchestration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ═══════════════════════════════════════════════════════════════════════════
// Mocks
// ═══════════════════════════════════════════════════════════════════════════

// mockScheduleStore is an in-test ScheduleStore implementation with hooks for
// injecting errors and tracking method calls.
type mockScheduleStore struct {
	mu        sync.Mutex
	schedules map[string]*core.Schedule

	// error hooks — set these to force errors from specific methods
	createErr error
	getErr    error
	listErr   error
	updateErr error
	deleteErr error
	getDueErr error

	// call counters
	createCalls int
	updateCalls int
	deleteCalls int
	getDueCalls int
}

func newMockScheduleStore() *mockScheduleStore {
	return &mockScheduleStore{schedules: map[string]*core.Schedule{}}
}

func (m *mockScheduleStore) Create(_ context.Context, s *core.Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	if m.createErr != nil {
		return m.createErr
	}
	if _, exists := m.schedules[s.ID]; exists {
		return core.ErrScheduleAlreadyExists
	}
	// Store a copy so tests can mutate the input without affecting the store.
	cp := *s
	m.schedules[s.ID] = &cp
	return nil
}

func (m *mockScheduleStore) Get(_ context.Context, id string) (*core.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	s, ok := m.schedules[id]
	if !ok {
		return nil, core.ErrScheduleNotFound
	}
	cp := *s
	return &cp, nil
}

func (m *mockScheduleStore) List(_ context.Context) ([]*core.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.listErr != nil {
		return nil, m.listErr
	}
	out := make([]*core.Schedule, 0, len(m.schedules))
	for _, s := range m.schedules {
		cp := *s
		out = append(out, &cp)
	}
	return out, nil
}

func (m *mockScheduleStore) Update(_ context.Context, s *core.Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.updateCalls++
	if m.updateErr != nil {
		return m.updateErr
	}
	if _, ok := m.schedules[s.ID]; !ok {
		return core.ErrScheduleNotFound
	}
	cp := *s
	m.schedules[s.ID] = &cp
	return nil
}

func (m *mockScheduleStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleteCalls++
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if _, ok := m.schedules[id]; !ok {
		return core.ErrScheduleNotFound
	}
	delete(m.schedules, id)
	return nil
}

func (m *mockScheduleStore) GetDue(_ context.Context, now time.Time) ([]*core.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getDueCalls++
	if m.getDueErr != nil {
		return nil, m.getDueErr
	}
	out := []*core.Schedule{}
	for _, s := range m.schedules {
		if s.Enabled && !s.RunAt.After(now) {
			cp := *s
			out = append(out, &cp)
		}
	}
	return out, nil
}

// schedulerTestTaskStore is an in-test TaskStore tailored for Scheduler tests.
// It wraps core.ErrTaskAlreadyExists on duplicate Create (matching
// RedisTaskStore semantics after the Phase 1a-bis surgical fix). The existing
// mockTaskStore in task_api_test.go does NOT wrap this sentinel, so we
// define a dedicated mock here to avoid cross-contaminating that test
// file's behaviour.
type schedulerTestTaskStore struct {
	mu        sync.Mutex
	tasks     map[string]*core.Task
	createErr error
}

func newSchedulerTestTaskStore() *schedulerTestTaskStore {
	return &schedulerTestTaskStore{tasks: map[string]*core.Task{}}
}

func (m *schedulerTestTaskStore) Create(_ context.Context, t *core.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.createErr != nil {
		return m.createErr
	}
	if _, exists := m.tasks[t.ID]; exists {
		return fmt.Errorf("%w: %s", core.ErrTaskAlreadyExists, t.ID)
	}
	cp := *t
	m.tasks[t.ID] = &cp
	return nil
}

func (m *schedulerTestTaskStore) Get(_ context.Context, id string) (*core.Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return nil, core.ErrTaskNotFound
	}
	cp := *t
	return &cp, nil
}

func (m *schedulerTestTaskStore) Update(_ context.Context, t *core.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.tasks[t.ID]; !ok {
		return core.ErrTaskNotFound
	}
	cp := *t
	m.tasks[t.ID] = &cp
	return nil
}

func (m *schedulerTestTaskStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tasks, id)
	return nil
}

func (m *schedulerTestTaskStore) Cancel(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.tasks[id]
	if !ok {
		return core.ErrTaskNotFound
	}
	t.Status = core.TaskStatusCancelled
	return nil
}

// mockTaskDispatcher records every Dispatch call so tests can assert on
// target queue, task IDs, and task payload.
type mockTaskDispatcher struct {
	mu         sync.Mutex
	dispatches []dispatchCall
	err        error
}

type dispatchCall struct {
	queueName string
	task      *core.Task
}

func (m *mockTaskDispatcher) Dispatch(_ context.Context, queueName string, task *core.Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	// Deep-ish copy of the task (shallow is fine for tests — we only read).
	cp := *task
	m.dispatches = append(m.dispatches, dispatchCall{queueName: queueName, task: &cp})
	return nil
}

func (m *mockTaskDispatcher) calls() []dispatchCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]dispatchCall, len(m.dispatches))
	copy(out, m.dispatches)
	return out
}

// mockLock is a programmable DistributedLock — tests control Acquire results.
type mockLock struct {
	mu           sync.Mutex
	acquired     bool
	acquireErr   error
	releaseErr   error
	acquireCalls int
	releaseCalls int
}

func (m *mockLock) Acquire(_ context.Context, _ string, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.acquireCalls++
	if m.acquireErr != nil {
		return false, m.acquireErr
	}
	return m.acquired, nil
}

func (m *mockLock) Release(_ context.Context, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releaseCalls++
	return m.releaseErr
}

// ═══════════════════════════════════════════════════════════════════════════
// NewScheduler — validation
// ═══════════════════════════════════════════════════════════════════════════

func TestNewScheduler_RequiresScheduleStore(t *testing.T) {
	_, err := NewScheduler(SchedulerDeps{
		TaskDispatcher: &mockTaskDispatcher{},
		TaskStore:      newSchedulerTestTaskStore(),
		Lock:           &mockLock{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ScheduleStore is required")
}

func TestNewScheduler_RequiresTaskDispatcher(t *testing.T) {
	_, err := NewScheduler(SchedulerDeps{
		ScheduleStore: newMockScheduleStore(),
		TaskStore:     newSchedulerTestTaskStore(),
		Lock:          &mockLock{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TaskDispatcher is required")
}

func TestNewScheduler_RequiresTaskStore(t *testing.T) {
	_, err := NewScheduler(SchedulerDeps{
		ScheduleStore:  newMockScheduleStore(),
		TaskDispatcher: &mockTaskDispatcher{},
		Lock:           &mockLock{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TaskStore is required")
}

func TestNewScheduler_RequiresLock(t *testing.T) {
	_, err := NewScheduler(SchedulerDeps{
		ScheduleStore:  newMockScheduleStore(),
		TaskDispatcher: &mockTaskDispatcher{},
		TaskStore:      newSchedulerTestTaskStore(),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Lock is required")
}

func TestNewScheduler_AppliesDefaults(t *testing.T) {
	s, err := NewScheduler(SchedulerDeps{
		ScheduleStore:  newMockScheduleStore(),
		TaskDispatcher: &mockTaskDispatcher{},
		TaskStore:      newSchedulerTestTaskStore(),
		Lock:           &mockLock{},
	})
	require.NoError(t, err)
	assert.NotNil(t, s.deps.Logger, "Logger should default to NoOpLogger")
	assert.Equal(t, defaultSchedulerTickInterval, s.deps.TickInterval)
	assert.Equal(t, defaultSchedulerLockTTL, s.deps.LockTTL)
	assert.NotEmpty(t, s.instanceID, "instance ID should be generated")
}

func TestNewScheduler_RespectsExplicitTimings(t *testing.T) {
	s, err := NewScheduler(SchedulerDeps{
		ScheduleStore:  newMockScheduleStore(),
		TaskDispatcher: &mockTaskDispatcher{},
		TaskStore:      newSchedulerTestTaskStore(),
		Lock:           &mockLock{},
		TickInterval:   2 * time.Second,
		LockTTL:        10 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, s.deps.TickInterval)
	assert.Equal(t, 10*time.Second, s.deps.LockTTL)
}

func TestNewScheduler_RejectsLockTTLSmallerThanTickInterval(t *testing.T) {
	_, err := NewScheduler(SchedulerDeps{
		ScheduleStore:  newMockScheduleStore(),
		TaskDispatcher: &mockTaskDispatcher{},
		TaskStore:      newSchedulerTestTaskStore(),
		Lock:           &mockLock{},
		TickInterval:   10 * time.Second,
		LockTTL:        5 * time.Second,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "LockTTL")
	assert.Contains(t, err.Error(), "must be greater than TickInterval")
}

// Compile-time sanity: Scheduler implements core.Runnable.
func TestScheduler_ImplementsRunnable(t *testing.T) {
	var _ core.Runnable = (*Scheduler)(nil)
}

// ═══════════════════════════════════════════════════════════════════════════
// tick — lock outcomes
// ═══════════════════════════════════════════════════════════════════════════

func newTestScheduler(t *testing.T, store *mockScheduleStore, taskStore *schedulerTestTaskStore, dispatcher *mockTaskDispatcher, lock *mockLock) *Scheduler {
	t.Helper()
	s, err := NewScheduler(SchedulerDeps{
		ScheduleStore:  store,
		TaskDispatcher: dispatcher,
		TaskStore:      taskStore,
		Lock:           lock,
		TickInterval:   100 * time.Millisecond,
		LockTTL:        1 * time.Second,
	})
	require.NoError(t, err)
	return s
}

func TestScheduler_Tick_NotLeader_DoesNothing(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: false}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	// Add a due schedule that would be promoted if we were leader.
	_ = store.Create(context.Background(), &core.Schedule{
		ID:          "sch-1",
		TargetAgent: "agent-a",
		RunAt:       time.Now().Add(-1 * time.Minute),
		Enabled:     true,
	})

	s.tick(context.Background())

	assert.Equal(t, 1, lock.acquireCalls)
	assert.Zero(t, store.getDueCalls, "non-leader must not query GetDue")
	assert.Empty(t, dispatcher.calls(), "non-leader must not dispatch")
}

func TestScheduler_Tick_LockError_DoesNothing(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquireErr: errors.New("redis down")}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	s.tick(context.Background())

	assert.Zero(t, store.getDueCalls, "lock error must skip GetDue")
	assert.Empty(t, dispatcher.calls())
}

func TestScheduler_Tick_GetDueError_DoesNothing(t *testing.T) {
	store := newMockScheduleStore()
	store.getDueErr = errors.New("store down")
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	s.tick(context.Background())

	assert.Equal(t, 1, store.getDueCalls)
	assert.Empty(t, dispatcher.calls())
}

// ═══════════════════════════════════════════════════════════════════════════
// tick — leader promotes due schedules
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduler_Tick_Leader_PromotesOneShotSchedule(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	pastTime := time.Now().Add(-5 * time.Minute)
	_ = store.Create(context.Background(), &core.Schedule{
		ID:              "sch-oneshot",
		TargetAgent:     "target-agent",
		RunAt:           pastTime,
		Enabled:         true,
		MissedRunPolicy: core.MissedRunSkip,
		Input:           map[string]interface{}{"instruction": "do it"},
	})

	s.tick(context.Background())

	// Task was dispatched exactly once.
	calls := dispatcher.calls()
	require.Len(t, calls, 1)
	assert.Equal(t, ScheduledExecutorQueue, calls[0].queueName)
	assert.Equal(t, core.ScheduledTaskType, calls[0].task.Type)
	assert.Equal(t, "sch-oneshot", calls[0].task.ScheduleID)
	assert.Equal(t, "target-agent", calls[0].task.TargetAgent, "task.TargetAgent should be stamped from schedule")
	expectedID := fmt.Sprintf("sch-oneshot:%d", pastTime.Unix())
	assert.Equal(t, expectedID, calls[0].task.ID)
	assert.Equal(t, "do it", calls[0].task.Input["instruction"])

	// Schedule was deleted (one-shot consumed).
	_, err := store.Get(context.Background(), "sch-oneshot")
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)
}

func TestScheduler_FireOnce_StampsTraceContext(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	pastTime := time.Now().Add(-5 * time.Minute)
	_ = store.Create(context.Background(), &core.Schedule{
		ID:              "sch-traced",
		TargetAgent:     "target-agent",
		RunAt:           pastTime,
		Enabled:         true,
		MissedRunPolicy: core.MissedRunSkip,
		Input:           map[string]interface{}{"instruction": "trace test"},
	})

	// Install a real tracer provider so StartLinkedSpan produces valid
	// span contexts (the default no-op tracer returns all-zeros which
	// GetTraceContext treats as invalid → empty strings).
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())
	otel.SetTracerProvider(tp)

	// Wrap the tick in a root span, mirroring what Scheduler.Start does.
	tickCtx, endTickSpan := telemetry.StartLinkedSpan(
		context.Background(),
		"scheduler.tick",
		"", "",
		map[string]string{"scheduler.instance_id": s.instanceID},
	)
	s.tick(tickCtx)
	endTickSpan()

	calls := dispatcher.calls()
	require.Len(t, calls, 1)
	task := calls[0].task

	assert.Equal(t, "target-agent", task.TargetAgent)
	assert.Equal(t, "sch-traced", task.ScheduleID)

	// The OTel SDK produces valid trace/span IDs even without an exporter.
	// They should be non-empty 32-char (trace) and 16-char (span) hex strings.
	assert.NotEmpty(t, task.TraceID, "task.TraceID should be stamped from the tick span")
	assert.NotEmpty(t, task.ParentSpanID, "task.ParentSpanID should be stamped from the tick span")
	assert.Len(t, task.TraceID, 32, "TraceID should be 32 hex chars")
	assert.Len(t, task.ParentSpanID, 16, "ParentSpanID should be 16 hex chars")
}

func TestScheduler_Tick_Leader_AdvancesRecurringSchedule(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	originalRunAt := time.Now().Add(-30 * time.Second)
	_ = store.Create(context.Background(), &core.Schedule{
		ID:              "sch-cron",
		TargetAgent:     "target-agent",
		CronExpr:        "*/1 * * * *", // every minute
		RunAt:           originalRunAt,
		Enabled:         true,
		MissedRunPolicy: core.MissedRunSkip,
	})

	s.tick(context.Background())

	require.Len(t, dispatcher.calls(), 1)

	// Schedule still exists with advanced RunAt.
	updated, err := store.Get(context.Background(), "sch-cron")
	require.NoError(t, err)
	assert.True(t, updated.RunAt.After(time.Now()), "RunAt should advance into the future")
	assert.NotNil(t, updated.LastRunAt, "LastRunAt should be populated")
}

func TestScheduler_Tick_Leader_SkipsFutureSchedule(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	_ = store.Create(context.Background(), &core.Schedule{
		ID:          "sch-future",
		TargetAgent: "target-agent",
		RunAt:       time.Now().Add(5 * time.Minute),
		Enabled:     true,
	})

	s.tick(context.Background())

	assert.Empty(t, dispatcher.calls(), "future schedule must not fire")
}

func TestScheduler_Tick_Leader_CatchUpPolicy_RoutesThroughFireCatchUp(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	// A recurring schedule with catchup policy, 2 minutes behind.
	pastTime := time.Now().Add(-2*time.Minute - 30*time.Second)
	_ = store.Create(context.Background(), &core.Schedule{
		ID:              "sch-catchup-tick",
		TargetAgent:     "agent-a",
		CronExpr:        "*/1 * * * *",
		RunAt:           pastTime,
		Enabled:         true,
		MissedRunPolicy: core.MissedRunCatchUp,
	})

	s.tick(context.Background())

	// Catchup should fire multiple tasks (not just one).
	assert.GreaterOrEqual(t, len(dispatcher.calls()), 2,
		"tick should route catchup schedules through fireCatchUp, firing multiple tasks")
}

func TestScheduler_Tick_Leader_SkipsDisabledSchedule(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	_ = store.Create(context.Background(), &core.Schedule{
		ID:          "sch-disabled",
		TargetAgent: "target-agent",
		RunAt:       time.Now().Add(-1 * time.Minute),
		Enabled:     false,
	})

	s.tick(context.Background())

	assert.Empty(t, dispatcher.calls(), "disabled schedule must not fire")
}

// ═══════════════════════════════════════════════════════════════════════════
// fireOnce — idempotency
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduler_FireOnce_Idempotent_DedupSkipsDispatch(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	fireTime := time.Now().Add(-1 * time.Minute)
	schedule := &core.Schedule{
		ID:          "sch-dedup",
		TargetAgent: "agent-a",
		RunAt:       fireTime,
		Enabled:     true,
		Input:       map[string]interface{}{"k": "v"},
	}

	// First fire — should succeed and dispatch.
	s.fireOnce(context.Background(), schedule, fireTime)
	require.Len(t, dispatcher.calls(), 1)

	// Second fire with identical fireTime — Create returns ErrTaskAlreadyExists,
	// dispatch must be skipped, no second dispatch recorded.
	s.fireOnce(context.Background(), schedule, fireTime)
	assert.Len(t, dispatcher.calls(), 1, "duplicate fire must not dispatch again")
}

func TestScheduler_FireOnce_TaskStoreError_NoDispatch(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	taskStore.createErr = errors.New("boom")
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	s.fireOnce(context.Background(), &core.Schedule{
		ID:          "sch-err",
		TargetAgent: "agent-a",
		RunAt:       time.Now(),
		Enabled:     true,
	}, time.Now())

	assert.Empty(t, dispatcher.calls(), "task store error must prevent dispatch")
}

func TestScheduler_FireOnce_DispatchError_TaskStillCreated(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{err: errors.New("dispatch failed")}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	fireTime := time.Now()
	schedule := &core.Schedule{
		ID:          "sch-dispatch-err",
		TargetAgent: "agent-a",
		RunAt:       fireTime,
		Enabled:     true,
	}

	s.fireOnce(context.Background(), schedule, fireTime)

	// Task should still be in the store even though dispatch failed. On the
	// next tick, Create will return ErrTaskAlreadyExists and we'll skip
	// dispatch rather than double-fire.
	expectedID := fmt.Sprintf("sch-dispatch-err:%d", fireTime.Unix())
	_, err := taskStore.Get(context.Background(), expectedID)
	assert.NoError(t, err, "task should be persisted even if dispatch fails")
}

func TestScheduler_FireOnce_InputIsCopied(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	fireTime := time.Now()
	schedule := &core.Schedule{
		ID:          "sch-copy",
		TargetAgent: "agent-a",
		RunAt:       fireTime,
		Enabled:     true,
		Input:       map[string]interface{}{"k": "v"},
	}

	s.fireOnce(context.Background(), schedule, fireTime)
	require.Len(t, dispatcher.calls(), 1)
	dispatchedTask := dispatcher.calls()[0].task

	// Mutate the dispatched task's Input — schedule's Input must be unchanged.
	dispatchedTask.Input["k"] = "mutated"
	assert.Equal(t, "v", schedule.Input["k"], "schedule.Input must not alias task.Input")
}

func TestScheduler_FireOnce_NilInput_IsHandled(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	fireTime := time.Now()
	s.fireOnce(context.Background(), &core.Schedule{
		ID:          "sch-nil",
		TargetAgent: "agent-a",
		RunAt:       fireTime,
		Enabled:     true,
		Input:       nil,
	}, fireTime)

	require.Len(t, dispatcher.calls(), 1)
	assert.Nil(t, dispatcher.calls()[0].task.Input)
}

// ═══════════════════════════════════════════════════════════════════════════
// fireCatchUp
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduler_FireCatchUp_FiresOncePerMissedInterval(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	// Schedule RunAt is 3 minutes ago, cron is every minute.
	// Expect 3 fires (the 3 missed runs) + 1 for the current minute,
	// depending on exact timing. Cursor iterates while cursor.Before(now).
	pastTime := time.Now().Add(-3*time.Minute - 30*time.Second)
	schedule := &core.Schedule{
		ID:              "sch-catchup",
		TargetAgent:     "agent-a",
		CronExpr:        "*/1 * * * *",
		RunAt:           pastTime,
		Enabled:         true,
		MissedRunPolicy: core.MissedRunCatchUp,
	}

	s.fireCatchUp(context.Background(), schedule)

	calls := dispatcher.calls()
	assert.GreaterOrEqual(t, len(calls), 2, "should fire at least 2 catchup runs")
	assert.Less(t, len(calls), 10, "catchup should bound at a reasonable count")

	// All fired tasks share the same ScheduleID but different task IDs.
	seen := map[string]bool{}
	for _, c := range calls {
		assert.Equal(t, "sch-catchup", c.task.ScheduleID)
		assert.False(t, seen[c.task.ID], "task IDs must be unique across catchup fires")
		seen[c.task.ID] = true
	}
}

func TestScheduler_FireCatchUp_OneShotSchedule_FiresOnce(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	fireTime := time.Now().Add(-5 * time.Minute)
	s.fireCatchUp(context.Background(), &core.Schedule{
		ID:              "sch-oneshot-catchup",
		TargetAgent:     "agent-a",
		CronExpr:        "", // one-shot
		RunAt:           fireTime,
		Enabled:         true,
		MissedRunPolicy: core.MissedRunCatchUp, // shouldn't matter for one-shot
	})

	// fireCatchUp defers to fireOnce for one-shot schedules.
	assert.Len(t, dispatcher.calls(), 1)
}

func TestScheduler_FireCatchUp_InvalidCron_StopsCleanly(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	// Invalid cron — the cursor iteration should fire once at RunAt then break
	// when nextCron returns an error. Not a panic, not an infinite loop.
	s.fireCatchUp(context.Background(), &core.Schedule{
		ID:              "sch-bad-cron",
		TargetAgent:     "agent-a",
		CronExpr:        "not-a-cron",
		RunAt:           time.Now().Add(-1 * time.Minute),
		Enabled:         true,
		MissedRunPolicy: core.MissedRunCatchUp,
	})

	// One fire happens before nextCron errors. Exact count is 1 because the
	// loop breaks on the first nextCron error.
	assert.Len(t, dispatcher.calls(), 1)
}

// ═══════════════════════════════════════════════════════════════════════════
// advanceOrDelete
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduler_AdvanceOrDelete_OneShot_Deletes(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	_ = store.Create(context.Background(), &core.Schedule{
		ID:          "sch-oneshot-del",
		TargetAgent: "agent-a",
		RunAt:       time.Now(),
		Enabled:     true,
	})

	sched, _ := store.Get(context.Background(), "sch-oneshot-del")
	s.advanceOrDelete(context.Background(), sched)

	_, err := store.Get(context.Background(), "sch-oneshot-del")
	assert.ErrorIs(t, err, core.ErrScheduleNotFound)
}

func TestScheduler_AdvanceOrDelete_Recurring_UpdatesRunAt(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	originalRunAt := time.Now().Add(-5 * time.Minute)
	_ = store.Create(context.Background(), &core.Schedule{
		ID:          "sch-recurring",
		TargetAgent: "agent-a",
		CronExpr:    "*/5 * * * *",
		RunAt:       originalRunAt,
		Enabled:     true,
	})

	sched, _ := store.Get(context.Background(), "sch-recurring")
	s.advanceOrDelete(context.Background(), sched)

	updated, err := store.Get(context.Background(), "sch-recurring")
	require.NoError(t, err)
	assert.True(t, updated.RunAt.After(originalRunAt))
	assert.True(t, updated.RunAt.After(time.Now()))
	require.NotNil(t, updated.LastRunAt)
}

func TestScheduler_AdvanceOrDelete_Recurring_InvalidCron_LeavesUnchanged(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	_ = store.Create(context.Background(), &core.Schedule{
		ID:          "sch-bad",
		TargetAgent: "agent-a",
		CronExpr:    "not-a-cron",
		RunAt:       time.Now(),
		Enabled:     true,
	})
	sched, _ := store.Get(context.Background(), "sch-bad")

	s.advanceOrDelete(context.Background(), sched)

	// Update should NOT have been called (0 updateCalls on the store)
	// because nextCron failed before Update.
	assert.Equal(t, 0, store.updateCalls)
	// Schedule is still there (not deleted).
	_, err := store.Get(context.Background(), "sch-bad")
	assert.NoError(t, err)
}

// ═══════════════════════════════════════════════════════════════════════════
// Start — ctx cancellation shuts down cleanly
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduler_Start_ExitsOnContextCancel(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// Cancel quickly; Start must return.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err, "Start should return nil on clean shutdown")
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}

	// Release was called during shutdown defer.
	assert.GreaterOrEqual(t, lock.releaseCalls, 1, "Lock.Release should be called on shutdown")
}

func TestScheduler_Start_LogsLockReleaseError(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	// Lock returns an error on Release; Start's deferred release should
	// log and swallow it (not panic or block shutdown).
	lock := &mockLock{acquired: true, releaseErr: errors.New("release failed")}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after ctx cancel")
	}
	assert.GreaterOrEqual(t, lock.releaseCalls, 1)
}

func TestScheduler_AdvanceOrDelete_OneShot_DeleteError_LogsOnly(t *testing.T) {
	store := newMockScheduleStore()
	store.deleteErr = errors.New("delete down")
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	// Pass a one-shot schedule directly — advanceOrDelete will try to Delete
	// and hit the injected error. Must not panic.
	s.advanceOrDelete(context.Background(), &core.Schedule{
		ID:          "sch-err-delete",
		TargetAgent: "agent-a",
		RunAt:       time.Now(),
		Enabled:     true,
		// CronExpr == "" → one-shot
	})
	assert.Equal(t, 1, store.deleteCalls)
}

func TestScheduler_AdvanceOrDelete_Recurring_UpdateError_LogsOnly(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	// Create the schedule so Update is attempted. Then inject updateErr.
	_ = store.Create(context.Background(), &core.Schedule{
		ID:          "sch-err-update",
		TargetAgent: "agent-a",
		CronExpr:    "*/5 * * * *",
		RunAt:       time.Now().Add(-1 * time.Minute),
		Enabled:     true,
	})
	store.updateErr = errors.New("update down")

	sched, _ := store.Get(context.Background(), "sch-err-update")
	s.advanceOrDelete(context.Background(), sched)
	// Update was called (and failed) but we didn't panic.
	assert.GreaterOrEqual(t, store.updateCalls, 1)
}

func TestScheduler_Start_RunsTickPeriodically(t *testing.T) {
	store := newMockScheduleStore()
	taskStore := newSchedulerTestTaskStore()
	dispatcher := &mockTaskDispatcher{}
	lock := &mockLock{acquired: true}
	s := newTestScheduler(t, store, taskStore, dispatcher, lock)

	// Add a schedule so we can observe tick activity.
	_ = store.Create(context.Background(), &core.Schedule{
		ID:          "sch-tick-test",
		TargetAgent: "agent-a",
		CronExpr:    "*/1 * * * *",
		RunAt:       time.Now().Add(-1 * time.Minute),
		Enabled:     true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Start(ctx) }()

	// Let the ticker fire at least once (tick interval is 100ms).
	time.Sleep(250 * time.Millisecond)
	cancel()
	<-done

	// At least one GetDue call happened from the tick loop.
	assert.GreaterOrEqual(t, store.getDueCalls, 1)
}

// ═══════════════════════════════════════════════════════════════════════════
// nextCron
// ═══════════════════════════════════════════════════════════════════════════

func TestScheduler_NextCron_Valid(t *testing.T) {
	s := newTestScheduler(t, newMockScheduleStore(), newSchedulerTestTaskStore(), &mockTaskDispatcher{}, &mockLock{})
	ref := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)
	next, err := s.nextCron("*/5 * * * *", ref)
	require.NoError(t, err)
	// Next "*/5 * * * *" after 12:00 is 12:05.
	assert.Equal(t, time.Date(2026, 4, 8, 12, 5, 0, 0, time.UTC), next)
}

func TestScheduler_NextCron_Invalid(t *testing.T) {
	s := newTestScheduler(t, newMockScheduleStore(), newSchedulerTestTaskStore(), &mockTaskDispatcher{}, &mockLock{})
	_, err := s.nextCron("not-a-cron", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse cron expression")
}

func TestScheduler_NextCron_NoFutureFire(t *testing.T) {
	// "0 0 31 2 *" parses successfully but Feb 31 never exists, so
	// robfig/cron returns a zero time. nextCron must convert this to an error.
	s := newTestScheduler(t, newMockScheduleStore(), newSchedulerTestTaskStore(), &mockTaskDispatcher{}, &mockLock{})
	_, err := s.nextCron("0 0 31 2 *", time.Now())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no next fire time")
}

// ═══════════════════════════════════════════════════════════════════════════
// resolveDurationEnv
// ═══════════════════════════════════════════════════════════════════════════

func TestResolveDurationEnv_Unset_ReturnsFallback(t *testing.T) {
	t.Setenv("TRUVAG3_TEST_ENV_DURATION", "")
	got := resolveDurationEnv("TRUVAG3_TEST_ENV_DURATION", 7*time.Second)
	assert.Equal(t, 7*time.Second, got)
}

func TestResolveDurationEnv_ValidGoDuration(t *testing.T) {
	t.Setenv("TRUVAG3_TEST_ENV_DURATION", "15s")
	got := resolveDurationEnv("TRUVAG3_TEST_ENV_DURATION", 7*time.Second)
	assert.Equal(t, 15*time.Second, got)
}

func TestResolveDurationEnv_BareInteger_RejectedFallsBack(t *testing.T) {
	// Bare integers are intentionally rejected — see comment in resolveDurationEnv.
	t.Setenv("TRUVAG3_TEST_ENV_DURATION", "5")
	got := resolveDurationEnv("TRUVAG3_TEST_ENV_DURATION", 7*time.Second)
	assert.Equal(t, 7*time.Second, got, "bare integers must fall back to default")
}

func TestResolveDurationEnv_Malformed_FallsBack(t *testing.T) {
	t.Setenv("TRUVAG3_TEST_ENV_DURATION", "garbage")
	got := resolveDurationEnv("TRUVAG3_TEST_ENV_DURATION", 7*time.Second)
	assert.Equal(t, 7*time.Second, got)
}

func TestResolveDurationEnv_NegativeDuration_FallsBack(t *testing.T) {
	t.Setenv("TRUVAG3_TEST_ENV_DURATION", "-5s")
	got := resolveDurationEnv("TRUVAG3_TEST_ENV_DURATION", 7*time.Second)
	assert.Equal(t, 7*time.Second, got)
}

// ═══════════════════════════════════════════════════════════════════════════
// buildInstanceID
// ═══════════════════════════════════════════════════════════════════════════

func TestBuildInstanceID_Fallback(t *testing.T) {
	// Clear the K8s service env var for this test.
	t.Setenv(core.EnvServiceName, "")
	id := buildInstanceID()
	assert.NotEmpty(t, id)
}

func TestBuildInstanceID_WithServiceName(t *testing.T) {
	t.Setenv(core.EnvServiceName, "scheduler-tool")
	id := buildInstanceID()
	assert.Contains(t, id, "scheduler-tool")
}
