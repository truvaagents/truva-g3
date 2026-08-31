package orchestration

import (
	"context"
	"encoding/json"
	"errors"
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
	RetentionTTL       time.Duration
}

type executionRecorder struct {
	mu         sync.Mutex
	tail       <-chan struct{}
	lifetime   context.Context
	store      ExecutionStore
	debugStore LLMDebugStore
	logger     core.Logger
	wg         *sync.WaitGroup
	timeout    time.Duration

	retentionMu     sync.Mutex
	retentionFloors map[string]time.Duration
}

func newExecutionRecorder(
	lifetime context.Context,
	store ExecutionStore,
	debugStore LLMDebugStore,
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
	return &executionRecorder{
		lifetime:        lifetime,
		store:           store,
		debugStore:      debugStore,
		logger:          logger,
		wg:              wg,
		timeout:         timeout,
		retentionFloors: make(map[string]time.Duration),
	}
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
		storeErr := r.store.Store(storeCtx, snapshot.Record)
		cancel()
		if storeErr != nil {
			r.logFailure(snapshot, storeErr, "store_write")
		}
		r.preserveLLMDebugRetention(base, snapshot)
	}()
}

func (r *executionRecorder) preserveLLMDebugRetention(
	ctx context.Context,
	snapshot executionRecordSnapshot,
) {
	if r == nil || r.debugStore == nil || snapshot.RetentionTTL <= 0 {
		return
	}
	requestIDs := []string{snapshot.RequestID}
	if rootID := relatedRootID(snapshot.Record); rootID != "" {
		requestIDs = append(requestIDs, rootID)
	}
	if preserver, ok := r.debugStore.(LLMDebugRetentionPreserver); ok {
		for _, requestID := range requestIDs {
			if !r.retentionIncreaseNeeded(requestID, snapshot.RetentionTTL) {
				continue
			}
			retentionCtx, cancel := context.WithTimeout(ctx, r.timeout)
			err := preserver.PreserveRetention(
				retentionCtx,
				requestID,
				snapshot.RetentionTTL,
			)
			cancel()
			if err != nil {
				r.logLLMRetentionFailure(snapshot, requestID, err)
				continue
			}
			r.rememberRetentionFloor(requestID, snapshot.RetentionTTL)
		}
		return
	}

	// Compatibility path for custom stores that have not implemented the
	// retention-floor capability. Built-in stateful stores use PreserveRetention.
	for _, requestID := range requestIDs {
		if !r.retentionIncreaseNeeded(requestID, snapshot.RetentionTTL) {
			continue
		}
		retentionCtx, cancel := context.WithTimeout(ctx, r.timeout)
		err := r.debugStore.ExtendTTL(retentionCtx, requestID, snapshot.RetentionTTL)
		cancel()
		if err != nil {
			if errors.Is(err, ErrLLMDebugRecordNotFound) {
				continue
			}
			r.logLLMRetentionFailure(snapshot, requestID, err)
			continue
		}
		r.rememberRetentionFloor(requestID, snapshot.RetentionTTL)
	}
}

func (r *executionRecorder) retentionIncreaseNeeded(
	requestID string,
	duration time.Duration,
) bool {
	r.retentionMu.Lock()
	defer r.retentionMu.Unlock()
	return r.retentionFloors[requestID] < duration
}

func (r *executionRecorder) rememberRetentionFloor(
	requestID string,
	duration time.Duration,
) {
	r.retentionMu.Lock()
	defer r.retentionMu.Unlock()
	if r.retentionFloors == nil {
		r.retentionFloors = make(map[string]time.Duration)
	}
	if r.retentionFloors[requestID] < duration {
		r.retentionFloors[requestID] = duration
	}
}

func (r *executionRecorder) logLLMRetentionFailure(
	snapshot executionRecordSnapshot,
	requestID string,
	err error,
) {
	if r == nil || r.logger == nil {
		return
	}
	r.logger.Warn("Failed to preserve LLM debug evidence retention", map[string]interface{}{
		"operation":            "llm_debug_lineage_retention",
		"request_id":           requestID,
		"execution_request_id": snapshot.RequestID,
		"error_type":           "retention_extension",
		"error":                safeExecutionStoreError(err),
	})
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
