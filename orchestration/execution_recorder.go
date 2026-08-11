package orchestration

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
)

const defaultExecutionStoreWriteTimeout = 5 * time.Second

// executionRecordSnapshot is an immutable request-local persistence view.
// Record is deep-cloned before it is accepted by the recorder.
type executionRecordSnapshot struct {
	Record             *StoredExecution
	CorrelationContext context.Context
	RequestID          string
	TraceID            string
	ConversationID     string
	CheckpointID       string
	Interrupted        bool
}

type executionRecorder struct {
	mu       sync.Mutex
	tail     <-chan struct{}
	lifetime context.Context
	store    ExecutionStore
	logger   core.Logger
	wg       *sync.WaitGroup
	timeout  time.Duration
}

func newExecutionRecorder(
	lifetime context.Context,
	store ExecutionStore,
	logger core.Logger,
	wg *sync.WaitGroup,
	timeout time.Duration,
) *executionRecorder {
	if lifetime == nil {
		lifetime = context.Background()
	}
	if timeout <= 0 {
		timeout = defaultExecutionStoreWriteTimeout
	}
	return &executionRecorder{lifetime: lifetime, store: store, logger: logger, wg: wg, timeout: timeout}
}

func cloneStoredExecution(source *StoredExecution) (*StoredExecution, error) {
	if source == nil {
		return nil, nil
	}
	encoded, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	var cloned StoredExecution
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}

func (r *executionRecorder) Record(snapshot executionRecordSnapshot) {
	if r == nil || r.store == nil || snapshot.Record == nil {
		return
	}
	copy, err := cloneStoredExecution(snapshot.Record)
	if err != nil {
		r.logFailure(snapshot, err, "marshal")
		return
	}
	snapshot.Record = copy

	r.mu.Lock()
	previous := r.tail
	completed := make(chan struct{})
	r.tail = completed
	if r.wg != nil {
		r.wg.Add(1)
	}
	r.mu.Unlock()

	go func() {
		if r.wg != nil {
			defer r.wg.Done()
		}
		defer close(completed)
		if previous != nil {
			select {
			case <-previous:
			case <-r.lifetime.Done():
				return
			}
		}
		if r.lifetime.Err() != nil {
			return
		}

		base := telemetry.CopyBaggage(r.lifetime, snapshot.CorrelationContext)
		storeCtx, cancel := context.WithTimeout(base, r.timeout)
		defer cancel()
		if err := r.store.Store(storeCtx, snapshot.Record); err != nil {
			r.logFailure(snapshot, err, "store_write")
		}
	}()
}

func (r *executionRecorder) logFailure(snapshot executionRecordSnapshot, err error, errorType string) {
	if r == nil || r.logger == nil {
		return
	}
	fields := map[string]interface{}{
		"operation":   "execution_store",
		"request_id":  snapshot.RequestID,
		"interrupted": snapshot.Interrupted,
		"error_type":  errorType,
		"error":       safeExecutionStoreError(err),
	}
	if snapshot.TraceID != "" {
		fields["trace_id"] = snapshot.TraceID
	}
	if snapshot.ConversationID != "" {
		fields[MetadataConversationID] = snapshot.ConversationID
	}
	if snapshot.CheckpointID != "" {
		fields["checkpoint_id"] = snapshot.CheckpointID
	}
	message := "Failed to store execution for DAG visualization"
	if errorType == "marshal" {
		message = "Failed to serialize execution for DAG visualization"
	}
	r.logger.Warn(message, fields)
}
