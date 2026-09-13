package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/orchestration"
)

// MarshalJSON projects framework checkpoint fields only at the wire boundary.
func (response SyncResponse) MarshalJSON() ([]byte, error) {
	type responseFields SyncResponse
	return json.Marshal(struct {
		responseFields
		Checkpoint *orchestration.CheckpointResponse `json:"checkpoint,omitempty"`
	}{responseFields: responseFields(response), Checkpoint: orchestration.CheckpointResponseFrom(response.Checkpoint)})
}

func (t *HITLChatAgent) resumeSession(checkpoint *orchestration.ExecutionCheckpoint) (string, bool) {
	id := sessionIDFromCheckpoint(checkpoint.UserContext)
	if id != "" {
		return id, false
	}
	return t.sessionStore.Create("", nil).ID, true
}

func (t *HITLChatAgent) executeApprovedCheckpoint(ctx context.Context, checkpoint *orchestration.ExecutionCheckpoint) (*orchestration.ExecutionResult, error) {
	_, execution, err := t.executeApprovedCheckpointSync(ctx, checkpoint)
	return execution, err
}

func (t *HITLChatAgent) executeApprovedCheckpointSync(ctx context.Context, checkpoint *orchestration.ExecutionCheckpoint) (*SyncResponse, *orchestration.ExecutionResult, error) {
	sessionID, _ := t.resumeSession(checkpoint)
	response, execution, err := t.processSyncWithExecution(ctx, sessionID, checkpoint.OriginalRequest, checkpoint.UserContext)
	if err != nil {
		return response, execution, err
	}
	if response == nil {
		return nil, execution, errors.New("resume processing returned no response")
	}
	response.OriginalRequestID = checkpoint.OriginalRequestID
	if response.Interrupted {
		if response.Checkpoint == nil {
			return response, execution, errors.New("resume processing returned no interruption checkpoint")
		}
		return response, execution, orchestration.NewInterruptError(response.Checkpoint)
	}
	return response, execution, nil
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
	mu        sync.Mutex
	requestID string
	tools     []string
	duration  int64
}

func (c *terminalResumeCallback) SendDone(requestID string, tools []string, duration int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.requestID, c.tools, c.duration = requestID, append([]string(nil), tools...), duration
}
func (*terminalResumeCallback) SendCheckpoint(*orchestration.ExecutionCheckpoint) {}
func (*terminalResumeCallback) SendError(string, string, bool)                    {}
func (c *terminalResumeCallback) Err() error {
	if delivery, ok := c.StreamCallback.(interface{ Err() error }); ok {
		return delivery.Err()
	}
	return nil
}
func (c *terminalResumeCallback) complete() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.StreamCallback.SendDone(c.requestID, c.tools, c.duration)
}

func (t *HITLChatAgent) serveResumeSSE(w http.ResponseWriter, r *http.Request, checkpointID string, expiryOnly bool) {
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
	startStream := func(ctx context.Context) {
		if callback != nil {
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		// Terminal delivery outlives the joined executor context, but never the
		// HTTP request. A claim-loss error can still be sent to a connected client.
		callback = NewSSECallbackWithLogger(w, flusher, t.Logger, r.Context())
		terminal = &terminalResumeCallback{StreamCallback: callback}
	}
	_, err := hitl.Resumer.ResumeWithExecutor(r.Context(), checkpointID,
		orchestration.ResumeExecutorFunc(func(ctx context.Context, checkpoint *orchestration.ExecutionCheckpoint) (*orchestration.ExecutionResult, error) {
			startStream(ctx)
			sessionID, created := t.resumeSession(checkpoint)
			if created {
				callback.sendEvent("session", map[string]interface{}{"id": sessionID})
			}
			callback.SendStatus("resuming", "Resuming execution after approval...")
			return t.processWithStreamingExecution(ctx, sessionID, checkpoint.OriginalRequest, terminal)
		}))
	var lifecycle *orchestration.ErrCheckpointResumeLifecycle
	if err != nil && !errors.As(err, &lifecycle) && orchestration.IsInterrupted(err) {
		startStream(r.Context()) // Recovery may find a child without invoking the executor.
		callback.SendCheckpoint(orchestration.GetCheckpoint(err))
		return
	}
	if err != nil {
		if callback == nil {
			writeResumeError(w, err)
			return
		}
		_, code, message := orchestration.HITLErrorResponse(err)
		callback.SendError(code, message, false)
		return
	}
	if terminal != nil {
		terminal.complete()
	}
}

func (t *HITLChatAgent) serveResumeSync(w http.ResponseWriter, r *http.Request) {
	setCORSHeaders(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "Only POST requests are supported", nil)
		return
	}
	id := extractPathParam(r.URL.Path, "/hitl/resume-sync/")
	if id == "" || strings.Contains(id, "/") {
		writeResumeError(w, &orchestration.ErrInvalidResumeRequest{Field: "checkpoint_id"})
		return
	}
	hitl := t.GetHITL()
	if hitl == nil || hitl.Resumer == nil {
		writeError(w, http.StatusServiceUnavailable, "HITL infrastructure not available", nil)
		return
	}
	started := time.Now()
	var response *SyncResponse
	_, err := hitl.Resumer.ResumeWithExecutor(r.Context(), id, orchestration.ResumeExecutorFunc(func(ctx context.Context, checkpoint *orchestration.ExecutionCheckpoint) (*orchestration.ExecutionResult, error) {
		var execution *orchestration.ExecutionResult
		var executeErr error
		response, execution, executeErr = t.executeApprovedCheckpointSync(ctx, checkpoint)
		return execution, executeErr
	}))
	var lifecycle *orchestration.ErrCheckpointResumeLifecycle
	if err != nil && !errors.As(err, &lifecycle) && orchestration.IsInterrupted(err) {
		checkpoint := orchestration.GetCheckpoint(err)
		if response == nil {
			response = &SyncResponse{RequestID: checkpoint.RequestID, SessionID: sessionIDFromCheckpoint(checkpoint.UserContext), OriginalRequestID: checkpoint.OriginalRequestID}
		}
		response.Checkpoint, response.Interrupted, response.DurationMs = checkpoint, true, time.Since(started).Milliseconds()
		writeJSON(w, http.StatusAccepted, response)
		return
	}
	if err != nil {
		writeResumeError(w, err)
		return
	}
	if response == nil {
		writeResumeError(w, &orchestration.ErrCheckpointExecutionFailed{})
		return
	}
	writeJSON(w, http.StatusOK, response)
}
