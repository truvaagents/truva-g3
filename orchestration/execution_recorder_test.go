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
	recorder := newExecutionRecorder(lifetime, store, nil, &wg, time.Second)

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
	recorder := newExecutionRecorder(lifetime, store, nil, &wg, time.Second)
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
