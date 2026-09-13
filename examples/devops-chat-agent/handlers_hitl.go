package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

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
	mu        sync.Mutex
	requestID string
	tools     []string
	duration  int64
	response  *orchestration.OrchestratorResponse
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
func (c *terminalResumeCallback) complete() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.StreamCallback.SendDone(c.requestID, c.tools, c.duration, c.response)
}

func (t *DevOpsChatAgent) serveResumeSSE(w http.ResponseWriter, r *http.Request, checkpointID string, expiryOnly bool) {
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

func (t *DevOpsChatAgent) handleResumeSSE(w http.ResponseWriter, r *http.Request) {
	t.serveResumeSSE(w, r, extractPathParam(r.URL.Path, "/hitl/resume/"), false)
}

func (t *DevOpsChatAgent) handleAutoResumeSSE(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(extractPathParam(r.URL.Path, "/hitl/auto-resume/"), "/stream")
	t.serveResumeSSE(w, r, id, true)
}
