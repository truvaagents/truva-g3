package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

func sessionIDFromCheckpoint(metadata map[string]interface{}) string {
	if conversationID, ok := metadata[orchestration.MetadataConversationID].(string); ok &&
		core.ValidateConversationID(conversationID) == core.ConversationIDValidationNone {
		return conversationID
	}
	if sessionID, ok := metadata["session_id"].(string); ok && sessionID != "" {
		return sessionID
	}
	return ""
}

func (t *DevOpsChatAgent) resumeSession(checkpoint *orchestration.ExecutionCheckpoint) (string, bool) {
	id := sessionIDFromCheckpoint(checkpoint.UserContext)
	if id != "" {
		return id, false
	}
	return t.sessionStore.Create("", nil).ID, true
}

func (t *DevOpsChatAgent) executeApprovedCheckpoint(ctx context.Context, checkpoint *orchestration.ExecutionCheckpoint) (*orchestration.ExecutionResult, error) {
	_, execution, err := t.executeApprovedCheckpointResponse(ctx, checkpoint)
	return execution, err
}

func (t *DevOpsChatAgent) executeApprovedCheckpointResponse(ctx context.Context, checkpoint *orchestration.ExecutionCheckpoint) (*orchestration.OrchestratorResponse, *orchestration.ExecutionResult, error) {
	t.mu.RLock()
	orch := t.orchestrator
	t.mu.RUnlock()
	if orch == nil {
		return nil, nil, errors.New("orchestrator not initialized")
	}
	metadata := make(map[string]interface{}, len(checkpoint.UserContext))
	for key, value := range checkpoint.UserContext {
		metadata[key] = value
	}
	sessionID := sessionIDFromCheckpoint(checkpoint.UserContext)
	if sessionID != "" {
		t.addConversationHistoryMetadata(metadata, sessionID, t.sessionStore.GetHistory(sessionID))
	}
	return orch.ProcessRequestWithExecution(ctx, checkpoint.OriginalRequest, metadata)
}

func writeResumeError(w http.ResponseWriter, err error) {
	status, code, message := orchestration.HITLErrorResponse(err)
	writeJSON(w, status, map[string]interface{}{"error": message, "code": code})
}

// terminalResumeCallback forwards progress but holds terminal notifications until
// the coordinator has durably finalized the parent. A saved successor is reloaded
// by the coordinator, not trusted from the processing callback.
type terminalResumeCallback struct {
	StreamCallback
	mu             sync.Mutex
	requestID      string
	tools          []string
	duration       int64
	response       *orchestration.OrchestratorResponse
	usage          *[3]int
	finish         string
	completion     func(error)
	completionOnce sync.Once
}

func (c *terminalResumeCallback) SendUsage(prompt, completion, total int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.usage = &[3]int{prompt, completion, total}
}
func (c *terminalResumeCallback) SendFinish(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.finish = reason
}

func (c *terminalResumeCallback) SendDone(requestID string, tools []string, duration int64, response *orchestration.OrchestratorResponse) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestID, c.tools, c.duration = requestID, append([]string(nil), tools...), duration
	c.response = response
}
func (*terminalResumeCallback) SendCheckpoint(*orchestration.ExecutionCheckpoint) {}
func (*terminalResumeCallback) SendError(string, string, bool)                    {}
func (c *terminalResumeCallback) Err() error {
	if delivery, ok := c.StreamCallback.(interface{ Err() error }); ok {
		return delivery.Err()
	}
	return nil
}
func (c *terminalResumeCallback) deferCompletion(completion func(error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.completion = completion
}
func (c *terminalResumeCallback) finishCompletion(err error) {
	c.completionOnce.Do(func() {
		c.mu.Lock()
		completion := c.completion
		c.mu.Unlock()
		if completion != nil {
			completion(err)
		}
	})
}
func (c *terminalResumeCallback) complete() error {
	c.mu.Lock()
	requestID, tools, duration, response, usage, finish := c.requestID, c.tools, c.duration, c.response, c.usage, c.finish
	c.mu.Unlock()
	if usage != nil {
		c.StreamCallback.SendUsage(usage[0], usage[1], usage[2])
	}
	if finish != "" {
		c.StreamCallback.SendFinish(finish)
	}
	c.StreamCallback.SendDone(requestID, tools, duration, response)
	err := c.Err()
	c.finishCompletion(err)
	if err != nil {
		return &finalSSEDeliveryError{requestID: requestID, cause: err}
	}
	return nil
}

func (t *DevOpsChatAgent) serveResumeSSE(w http.ResponseWriter, r *http.Request, checkpointID string, expiryOnly bool) {
	startTime := time.Now()
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	method := http.MethodPost
	if expiryOnly {
		method = http.MethodGet
	}
	if r.Method != method {
		writeError(w, http.StatusMethodNotAllowed, "unsupported resume method", nil)
		return
	}
	if checkpointID == "" || strings.Contains(checkpointID, "/") {
		writeResumeError(w, &orchestration.ErrInvalidResumeRequest{Field: "checkpoint_id"})
		return
	}
	hitl := t.GetHITL()
	if hitl == nil || hitl.Resumer == nil {
		writeError(w, http.StatusServiceUnavailable, "HITL infrastructure not available", nil)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "SSE not supported", nil)
		return
	}
	if expiryOnly {
		// This existing route accepts timeout approval only. The read is a route
		// precondition, never an ownership check; the coordinator still claims
		// authoritatively and resolves any concurrent owner.
		checkpoint, err := hitl.CheckpointStore.LoadCheckpoint(r.Context(), checkpointID)
		if err != nil {
			writeResumeError(w, err)
			return
		}
		status := checkpoint.Status
		if status == orchestration.CheckpointStatusResuming && checkpoint.ResumeState != nil {
			status = checkpoint.ResumeState.PreviousStatus
		}
		if status != orchestration.CheckpointStatusExpiredApproved {
			writeResumeError(w, &orchestration.ErrCheckpointNotResumable{CheckpointID: checkpointID, Status: status})
			return
		}
	}
	var callback *SSECallback
	var terminal *terminalResumeCallback
	var sessionID string
	requestCtx, cancelRequest := context.WithCancel(r.Context())
	defer func() {
		cancelRequest()
		if callback != nil {
			callback.StopHeartbeat()
		}
	}()
	startStream := func() error {
		if callback != nil {
			return callback.Err()
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		// Terminal delivery outlives the joined executor context, but never the
		// HTTP request. A claim-loss error can still be sent to a connected client.
		callback = newManagedSSECallback(w, flusher, t.Logger, requestCtx, cancelRequest)
		if err := callback.StartHeartbeat(); err != nil {
			return err
		}
		terminal = &terminalResumeCallback{StreamCallback: callback}
		return nil
	}
	_, err := hitl.Resumer.ResumeWithExecutor(requestCtx, checkpointID,
		orchestration.ResumeExecutorFunc(func(ctx context.Context, checkpoint *orchestration.ExecutionCheckpoint) (*orchestration.ExecutionResult, error) {
			if err := startStream(); err != nil {
				return nil, err
			}
			var created bool
			sessionID, created = t.resumeSession(checkpoint)
			if created {
				callback.sendEvent("session", map[string]interface{}{"id": sessionID})
			}
			callback.SendStatus("resuming", "Resuming execution after approval...")
			return t.processWithStreamingExecution(ctx, sessionID, checkpoint.OriginalRequest, terminal)
		}))
	var lifecycle *orchestration.ErrCheckpointResumeLifecycle
	if err != nil && !errors.As(err, &lifecycle) && orchestration.IsInterrupted(err) {
		if err := startStream(); err != nil {
			return
		} // Recovery may find a child without invoking the executor.
		callback.SendCheckpoint(orchestration.GetCheckpoint(err))
		return
	}
	if err != nil {
		if terminal != nil {
			// Preserve generated content as undelivered on durable-finalization
			// failure; never publish a terminal success before that boundary.
			terminal.finishCompletion(err)
		}
		if callback == nil {
			writeResumeError(w, err)
			return
		}
		_, code, message := orchestration.HITLErrorResponse(err)
		callback.SendError(code, message, false)
		return
	}
	if terminal != nil {
		if deliveryErr := terminal.complete(); deliveryErr != nil {
			var final *finalSSEDeliveryError
			if errors.As(deliveryErr, &final) {
				t.recordFinalSSEDeliveryFailure(r.Context(), final, "hitl_resume", sessionID, time.Since(startTime))
			}
		}
	}
}

func (t *DevOpsChatAgent) handleResumeSSE(w http.ResponseWriter, r *http.Request) {
	t.serveResumeSSE(w, r, extractPathParam(r.URL.Path, "/hitl/resume/"), false)
}

func (t *DevOpsChatAgent) handleAutoResumeSSE(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(extractPathParam(r.URL.Path, "/hitl/auto-resume/"), "/stream")
	t.serveResumeSSE(w, r, id, true)
}
