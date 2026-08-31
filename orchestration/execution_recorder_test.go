package orchestration

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestExecutionRecorderClassifiesSerializationSeparatelyFromStoreWrites(t *testing.T) {
	logger := &TestLogger{}
	recorder := &executionRecorder{logger: logger}
	snapshot := executionRecordSnapshot{RequestID: "request-recorder"}
	recorder.logFailure(snapshot, errors.New("serialization detail"), "marshal")
	recorder.logFailure(snapshot, errors.New("backend detail"), "store_write")

	logs := logger.GetLogsByOperation("execution_store")
	if len(logs) != 2 || logs[0].Fields["error_type"] != "marshal" ||
		logs[1].Fields["error_type"] != "store_write" {
		t.Fatalf("recorder failure classifications = %#v", logs)
	}
}

type orderedExecutionStore struct {
	*NoOpExecutionStore
	mu            sync.Mutex
	started       []string
	stored        []*StoredExecution
	firstStarted  chan struct{}
	secondStarted chan struct{}
	releaseFirst  chan struct{}
}

func newOrderedExecutionStore() *orderedExecutionStore {
	return &orderedExecutionStore{
		NoOpExecutionStore: NewNoOpExecutionStore(),
		firstStarted:       make(chan struct{}),
		secondStarted:      make(chan struct{}),
		releaseFirst:       make(chan struct{}),
	}
}

func (s *orderedExecutionStore) Store(ctx context.Context, execution *StoredExecution) error {
	s.mu.Lock()
	index := len(s.started)
	s.started = append(s.started, execution.OriginalRequest)
	s.mu.Unlock()
	switch index {
	case 0:
		close(s.firstStarted)
		select {
		case <-s.releaseFirst:
		case <-ctx.Done():
			return ctx.Err()
		}
	case 1:
		close(s.secondStarted)
	}
	s.mu.Lock()
	s.stored = append(s.stored, execution)
	s.mu.Unlock()
	return nil
}

func TestExecutionRecorder_OrdersAndDefensivelyClonesRequestWrites(t *testing.T) {
	store := newOrderedExecutionStore()
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	recorder := newExecutionRecorder(lifetime, store, nil, nil, &wg, time.Second)

	first := &StoredExecution{RequestID: "request", OriginalRequest: "first"}
	recorder.Record(executionRecordSnapshot{Record: first, RequestID: "request"})
	<-store.firstStarted
	first.OriginalRequest = "mutated-after-record"
	recorder.Record(executionRecordSnapshot{
		Record: &StoredExecution{RequestID: "request", OriginalRequest: "second"}, RequestID: "request",
	})

	select {
	case <-store.secondStarted:
		t.Fatal("second write started before first completed")
	case <-time.After(25 * time.Millisecond):
	}
	close(store.releaseFirst)
	wg.Wait()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.stored) != 2 || store.stored[0].OriginalRequest != "first" || store.stored[1].OriginalRequest != "second" {
		t.Fatalf("stored ordered snapshots = %#v", store.stored)
	}
}

func TestExecutionRecorder_SharedLifetimeCancelsBlockedTail(t *testing.T) {
	store := newOrderedExecutionStore()
	lifetime, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	recorder := newExecutionRecorder(lifetime, store, nil, nil, &wg, time.Second)
	recorder.Record(executionRecordSnapshot{
		Record: &StoredExecution{RequestID: "request", OriginalRequest: "first"}, RequestID: "request",
	})
	<-store.firstStarted
	recorder.Record(executionRecordSnapshot{
		Record: &StoredExecution{RequestID: "request", OriginalRequest: "second"}, RequestID: "request",
	})
	cancel()
	wg.Wait()

	select {
	case <-store.secondStarted:
		t.Fatal("blocked tail started a new store timeout after lifetime cancellation")
	default:
	}
}

type contextBlockingExecutionStore struct {
	*NoOpExecutionStore
	started chan struct{}
	exited  chan struct{}
	once    sync.Once
}

func newContextBlockingExecutionStore() *contextBlockingExecutionStore {
	return &contextBlockingExecutionStore{
		NoOpExecutionStore: NewNoOpExecutionStore(),
		started:            make(chan struct{}),
		exited:             make(chan struct{}),
	}
}

func (s *contextBlockingExecutionStore) Store(ctx context.Context, _ *StoredExecution) error {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	close(s.exited)
	return ctx.Err()
}

func TestAIOrchestratorShutdown_CancelsConformingExecutionStore(t *testing.T) {
	store := newContextBlockingExecutionStore()
	recordingCtx, recordingCancel := context.WithCancel(context.Background())
	orchestrator := &AIOrchestrator{
		config:          &OrchestratorConfig{executionStoreWriteTimeout: time.Minute},
		executionStore:  store,
		recordingCtx:    recordingCtx,
		recordingCancel: recordingCancel,
		cancel:          func() {},
	}

	recorder := orchestrator.executionRecorderFor("shutdown-request")
	recorder.Record(executionRecordSnapshot{
		Record:    &StoredExecution{RequestID: "shutdown-request"},
		RequestID: "shutdown-request",
	})
	<-store.started

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := orchestrator.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want deadline exceeded", err)
	}

	select {
	case <-store.exited:
	case <-time.After(time.Second):
		t.Fatal("context-aware execution store did not exit after recorder lifetime cancellation")
	}
}

type contextIgnoringExecutionStore struct {
	*NoOpExecutionStore
	started chan struct{}
	release chan struct{}
	exited  chan struct{}
}

type retentionRecordingLLMDebugStore struct {
	*NoOpLLMDebugStore
	mu         sync.Mutex
	extensions map[string]time.Duration
	calls      map[string]int
	errors     map[string]error
	called     chan string
}

type delayedExecutionStore struct {
	*NoOpExecutionStore
	delay time.Duration
}

type failingExecutionStore struct {
	*NoOpExecutionStore
}

func (s *failingExecutionStore) Store(context.Context, *StoredExecution) error {
	return errors.New("execution persistence unavailable")
}

func (s *delayedExecutionStore) Store(ctx context.Context, _ *StoredExecution) error {
	select {
	case <-time.After(s.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type deadlineObservingRetentionStore struct {
	*NoOpLLMDebugStore
	remaining chan time.Duration
}

type firstTimeoutRetentionStore struct {
	*NoOpLLMDebugStore
	rootCalled chan struct{}
}

func (s *firstTimeoutRetentionStore) PreserveRetention(
	ctx context.Context,
	requestID string,
	_ time.Duration,
) error {
	if requestID == "timeout-child" {
		<-ctx.Done()
		return ctx.Err()
	}
	if requestID == "timeout-root" {
		close(s.rootCalled)
	}
	return nil
}

func (s *deadlineObservingRetentionStore) PreserveRetention(
	ctx context.Context,
	_ string,
	_ time.Duration,
) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("retention context has no deadline")
	}
	s.remaining <- time.Until(deadline)
	return nil
}

func newRetentionRecordingLLMDebugStore() *retentionRecordingLLMDebugStore {
	return &retentionRecordingLLMDebugStore{
		NoOpLLMDebugStore: NewNoOpLLMDebugStore(),
		extensions:        make(map[string]time.Duration),
		calls:             make(map[string]int),
		errors:            make(map[string]error),
		called:            make(chan string, 4),
	}
}

func (s *retentionRecordingLLMDebugStore) ExtendTTL(
	_ context.Context,
	requestID string,
	duration time.Duration,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.errors[requestID]; err != nil {
		return err
	}
	s.extensions[requestID] = duration
	s.calls[requestID]++
	s.called <- requestID
	return nil
}

func TestExecutionRecorderPreservesCurrentAndRootLLMEvidence(t *testing.T) {
	debugStore := newRetentionRecordingLLMDebugStore()
	var wg sync.WaitGroup
	recorder := newExecutionRecorder(
		context.Background(),
		NewNoOpExecutionStore(),
		debugStore,
		nil,
		&wg,
		time.Second,
	)
	recorder.Record(executionRecordSnapshot{
		Record: &StoredExecution{
			RequestID:         "resume-request",
			OriginalRequestID: "root-request",
		},
		RequestID:    "resume-request",
		RetentionTTL: 7 * 24 * time.Hour,
	})
	wg.Wait()

	debugStore.mu.Lock()
	defer debugStore.mu.Unlock()
	for _, requestID := range []string{"resume-request", "root-request"} {
		if got := debugStore.extensions[requestID]; got != 7*24*time.Hour {
			t.Fatalf("%s LLM retention = %v, want 7d", requestID, got)
		}
	}
}

func TestExecutionRecorderIgnoresTypedMissingLLMEvidence(t *testing.T) {
	debugStore := newRetentionRecordingLLMDebugStore()
	debugStore.errors["no-llm-request"] = ErrLLMDebugRecordNotFound
	logger := &TestLogger{}
	var wg sync.WaitGroup
	recorder := newExecutionRecorder(
		context.Background(),
		NewNoOpExecutionStore(),
		debugStore,
		logger,
		&wg,
		time.Second,
	)
	recorder.Record(executionRecordSnapshot{
		Record:       &StoredExecution{RequestID: "no-llm-request"},
		RequestID:    "no-llm-request",
		RetentionTTL: time.Hour,
	})
	wg.Wait()
	if logs := logger.GetLogsByOperation("llm_debug_lineage_retention"); len(logs) != 0 {
		t.Fatalf("typed missing LLM evidence emitted warnings: %#v", logs)
	}
}

func TestExecutionRecorderSkipsRedundantRetentionRoundTrips(t *testing.T) {
	debugStore := newRetentionRecordingLLMDebugStore()
	var wg sync.WaitGroup
	recorder := newExecutionRecorder(
		context.Background(),
		NewNoOpExecutionStore(),
		debugStore,
		nil,
		&wg,
		time.Second,
	)
	for _, ttl := range []time.Duration{time.Hour, time.Hour, 7 * time.Hour} {
		recorder.Record(executionRecordSnapshot{
			Record:       &StoredExecution{RequestID: "bounded-retention-writes"},
			RequestID:    "bounded-retention-writes",
			RetentionTTL: ttl,
		})
	}
	wg.Wait()

	debugStore.mu.Lock()
	defer debugStore.mu.Unlock()
	if got := debugStore.calls["bounded-retention-writes"]; got != 2 {
		t.Fatalf("retention calls = %d, want one initial write and one increase", got)
	}
	if got := debugStore.extensions["bounded-retention-writes"]; got != 7*time.Hour {
		t.Fatalf("final retention = %v, want 7h", got)
	}
}

func TestExecutionRecorderGivesRetentionAnIndependentTimeout(t *testing.T) {
	const timeout = 100 * time.Millisecond
	debugStore := &deadlineObservingRetentionStore{
		NoOpLLMDebugStore: NewNoOpLLMDebugStore(),
		remaining:         make(chan time.Duration, 1),
	}
	var wg sync.WaitGroup
	recorder := newExecutionRecorder(
		context.Background(),
		&delayedExecutionStore{
			NoOpExecutionStore: NewNoOpExecutionStore(),
			delay:              60 * time.Millisecond,
		},
		debugStore,
		nil,
		&wg,
		timeout,
	)
	recorder.Record(executionRecordSnapshot{
		Record:       &StoredExecution{RequestID: "independent-timeout"},
		RequestID:    "independent-timeout",
		RetentionTTL: time.Hour,
	})
	wg.Wait()

	select {
	case remaining := <-debugStore.remaining:
		if remaining < 70*time.Millisecond {
			t.Fatalf("retention inherited spent store timeout; remaining = %v", remaining)
		}
	default:
		t.Fatal("retention preservation was not called")
	}
}

func TestExecutionRecorderPreservesLLMEvidenceAfterExecutionStoreFailure(t *testing.T) {
	debugStore := &deadlineObservingRetentionStore{
		NoOpLLMDebugStore: NewNoOpLLMDebugStore(),
		remaining:         make(chan time.Duration, 1),
	}
	var wg sync.WaitGroup
	recorder := newExecutionRecorder(
		context.Background(),
		&failingExecutionStore{NoOpExecutionStore: NewNoOpExecutionStore()},
		debugStore,
		nil,
		&wg,
		time.Second,
	)
	recorder.Record(executionRecordSnapshot{
		Record:       &StoredExecution{RequestID: "failed-execution-store"},
		RequestID:    "failed-execution-store",
		RetentionTTL: 7 * 24 * time.Hour,
	})
	wg.Wait()

	select {
	case <-debugStore.remaining:
	default:
		t.Fatal("execution store failure skipped independent LLM retention")
	}
}

func TestExecutionRecorderUsesIndependentTimeoutForEachRetentionTarget(t *testing.T) {
	debugStore := &firstTimeoutRetentionStore{
		NoOpLLMDebugStore: NewNoOpLLMDebugStore(),
		rootCalled:        make(chan struct{}),
	}
	var wg sync.WaitGroup
	recorder := newExecutionRecorder(
		context.Background(),
		NewNoOpExecutionStore(),
		debugStore,
		nil,
		&wg,
		20*time.Millisecond,
	)
	recorder.Record(executionRecordSnapshot{
		Record: &StoredExecution{
			RequestID:         "timeout-child",
			OriginalRequestID: "timeout-root",
		},
		RequestID:    "timeout-child",
		RetentionTTL: time.Hour,
	})
	wg.Wait()

	select {
	case <-debugStore.rootCalled:
	default:
		t.Fatal("current-record timeout consumed the root retention deadline")
	}
}

func newContextIgnoringExecutionStore() *contextIgnoringExecutionStore {
	return &contextIgnoringExecutionStore{
		NoOpExecutionStore: NewNoOpExecutionStore(),
		started:            make(chan struct{}),
		release:            make(chan struct{}),
		exited:             make(chan struct{}),
	}
}

func (s *contextIgnoringExecutionStore) Store(_ context.Context, _ *StoredExecution) error {
	close(s.started)
	<-s.release
	close(s.exited)
	return nil
}

func TestAIOrchestratorShutdown_ContextIgnoringExecutionStoreRemainsCallerBounded(t *testing.T) {
	store := newContextIgnoringExecutionStore()
	recordingCtx, recordingCancel := context.WithCancel(context.Background())
	orchestrator := &AIOrchestrator{
		config:          &OrchestratorConfig{executionStoreWriteTimeout: time.Minute},
		executionStore:  store,
		recordingCtx:    recordingCtx,
		recordingCancel: recordingCancel,
		cancel:          func() {},
	}

	recorder := orchestrator.executionRecorderFor("nonconforming-request")
	recorder.Record(executionRecordSnapshot{
		Record:    &StoredExecution{RequestID: "nonconforming-request"},
		RequestID: "nonconforming-request",
	})
	<-store.started

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := orchestrator.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want deadline exceeded", err)
	}

	select {
	case <-store.exited:
		t.Fatal("context-ignoring execution store unexpectedly honored recorder cancellation")
	default:
	}
	close(store.release)
	select {
	case <-store.exited:
	case <-time.After(time.Second):
		t.Fatal("context-ignoring execution store did not exit after the test released it")
	}
}
