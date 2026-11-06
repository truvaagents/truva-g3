package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/orchestration"
)

type resumeSSEPersistence struct {
	mu                      sync.Mutex
	sessionID               string
	claimErr, finalizeErr   error
	recoverChild            bool
	finalized               bool
	child                   *orchestration.ExecutionCheckpoint
	finalizeCheck           func()
	releases, finalizations int
}

func (s *resumeSSEPersistence) ClaimCheckpointForResume(ctx context.Context, request orchestration.ResumeClaimRequest) (*orchestration.CheckpointResumeClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if s.finalized {
		return nil, &orchestration.ErrCheckpointNotResumable{CheckpointID: request.CheckpointID, Status: orchestration.CheckpointStatusCompleted}
	}
	state := &orchestration.CheckpointResumeState{AttemptID: request.AttemptID, Owner: request.Owner, PreviousStatus: orchestration.CheckpointStatusApproved, LeaseExpiresAt: time.Now().Add(request.Lease), ReservedSuccessorCheckpointID: request.SuccessorCheckpointID, ReservedSuccessorAttemptID: request.AttemptID}
	if s.recoverChild {
		s.child = &orchestration.ExecutionCheckpoint{CheckpointID: request.SuccessorCheckpointID, ParentCheckpointID: request.CheckpointID, ParentResumeAttemptID: request.AttemptID, Status: orchestration.CheckpointStatusPending}
	}
	terminal := true
	checkpoint := &orchestration.ExecutionCheckpoint{CheckpointID: request.CheckpointID, RequestID: "resume-original", OriginalRequest: "approved fixture", Status: orchestration.CheckpointStatusApproved, RequestMode: orchestration.RequestModeStreaming, UserContext: map[string]interface{}{orchestration.MetadataConversationID: s.sessionID}, ResumeState: state, Plan: &orchestration.RoutingPlan{PlanID: "approved-plan", OriginalRequest: "approved fixture", Mode: orchestration.ModeAutonomous, Steps: []orchestration.RoutingStep{}, Terminal: &terminal}}
	return &orchestration.CheckpointResumeClaim{Checkpoint: checkpoint, AttemptID: request.AttemptID, PreviousStatus: orchestration.CheckpointStatusApproved, LeaseExpiresAt: state.LeaseExpiresAt}, nil
}
func (s *resumeSSEPersistence) LoadCheckpoint(_ context.Context, id string) (*orchestration.ExecutionCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.child != nil && s.child.CheckpointID == id {
		return s.child, nil
	}
	return nil, &orchestration.ErrCheckpointNotFound{CheckpointID: id}
}
func (*resumeSSEPersistence) RenewCheckpointResumeClaim(context.Context, string, string, time.Duration) error {
	return nil
}
func (s *resumeSSEPersistence) ReleaseCheckpointResumeClaim(context.Context, string, string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releases++
	return nil
}
func (s *resumeSSEPersistence) FinalizeCheckpointResume(_ context.Context, _ orchestration.ResumeFinalization) error {
	if s.finalizeCheck != nil {
		s.finalizeCheck()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalizations++
	if s.finalizeErr != nil {
		return s.finalizeErr
	}
	s.finalized = true
	return nil
}
func newResumeSSECoordinator(t *testing.T, store *resumeSSEPersistence) *orchestration.ResumeCoordinator {
	t.Helper()
	coordinator, err := orchestration.NewResumeCoordinator(store, orchestration.ResumeExecutorFunc(func(context.Context, *orchestration.ExecutionCheckpoint) (*orchestration.ExecutionResult, error) {
		t.Error("shared executor was used")
		return nil, errors.New("unexpected shared executor")
	}), orchestration.DefaultResumeCoordinatorRuntimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func TestResumeSSEWaitsForDurableFinalization(t *testing.T) {
	for _, failure := range []string{"none", "finalization", "final delivery"} {
		t.Run(failure, func(t *testing.T) {
			client := &sseFixtureAI{}
			agent, sessionID, logger := newSSEAgentFixture(t, client)
			writer := newSSETestResponseWriter()
			store := &resumeSSEPersistence{sessionID: sessionID}
			if failure == "finalization" {
				store.finalizeErr = errors.New("fixture finalization failure")
			}
			if failure == "final delivery" {
				writer.failEvent = "done"
			}
			store.finalizeCheck = func() {
				body, _ := writer.snapshot()
				for _, event := range []string{"usage", "finish", "done"} {
					if strings.Contains(body, "event: "+event+"\n") {
						t.Errorf("%s escaped before durable finalization: %s", event, body)
					}
				}
				if len(agent.sessionStore.GetHistory(sessionID)) != 0 {
					t.Error("history claimed delivery before finalization")
				}
			}
			agent.hitl = &HITLInfrastructure{Resumer: newResumeSSECoordinator(t, store)}
			agent.serveResumeSSE(writer, httptest.NewRequest(http.MethodPost, "/hitl/resume/parent", nil), "parent", false)
			body, _ := writer.snapshot()
			if !strings.HasPrefix(body, ": heartbeat\n\n") || client.streamCalls != 1 || store.releases != 0 || store.finalizations != 1 {
				t.Fatalf("resume lifecycle: stream=%s calls=%d releases=%d finalizations=%d", body, client.streamCalls, store.releases, store.finalizations)
			}
			history := agent.sessionStore.GetHistory(sessionID)
			if len(history) != 1 || history[0].Content != "retained answer" {
				t.Fatalf("history lost: %#v", history)
			}
			if failure == "none" {
				if !strings.Contains(body, "event: usage\n") || !strings.Contains(body, "event: finish\n") || !strings.Contains(body, "event: done\n") || !logger.contains("Chat request completed") {
					t.Fatalf("missing successful completion: %s", body)
				}
			} else {
				if strings.Contains(body, "event: done\n") || history[0].Metadata["delivery_status"] != "failed" || logger.contains("Chat request completed") {
					t.Fatalf("failure became success: %s; history %#v", body, history)
				}
				if failure == "finalization" && !strings.Contains(body, "event: error\n") {
					t.Fatalf("executor cancellation prevented final error delivery: %s", body)
				}
				if failure == "final delivery" && !logger.contains("Final SSE delivery failed") {
					t.Fatal("missing final delivery accounting")
				}
			}
			if failure != "finalization" {
				second := httptest.NewRecorder()
				agent.serveResumeSSE(second, httptest.NewRequest(http.MethodPost, "/hitl/resume/parent", nil), "parent", false)
				if second.Code < 400 || strings.Contains(second.Body.String(), ": heartbeat") || client.streamCalls != 1 {
					t.Fatalf("finalized operation was replayed: %s", second.Body.String())
				}
			}
		})
	}
}

func TestResumeSSESuccessorRecoveryUsesHeartbeatWithoutExecution(t *testing.T) {
	store := &resumeSSEPersistence{recoverChild: true}
	agent := &DevOpsChatAgent{BaseAgent: core.NewBaseAgent("resume-recovery"), hitl: &HITLInfrastructure{Resumer: newResumeSSECoordinator(t, store)}}
	response := httptest.NewRecorder()
	agent.serveResumeSSE(response, httptest.NewRequest(http.MethodPost, "/hitl/resume/parent", nil), "parent", false)
	body := response.Body.String()
	if !strings.HasPrefix(body, ": heartbeat\n\n") || !strings.Contains(body, "event: checkpoint\n") || strings.Contains(body, "event: done\n") || !store.finalized || store.releases != 0 {
		t.Fatalf("successor recovery = %s", body)
	}
}

func TestResumeSSERejectionsRemainJSON(t *testing.T) {
	for _, test := range []struct {
		name, method, id string
		claimErr         error
	}{
		{"method", http.MethodGet, "parent", nil},
		{"invalid checkpoint", http.MethodPost, "bad/id", nil},
		{"claim", http.MethodPost, "parent", &orchestration.ErrCheckpointNotFound{CheckpointID: "parent"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &resumeSSEPersistence{claimErr: test.claimErr}
			agent := &DevOpsChatAgent{BaseAgent: core.NewBaseAgent("resume-rejection"), hitl: &HITLInfrastructure{Resumer: newResumeSSECoordinator(t, store)}}
			response := httptest.NewRecorder()
			agent.serveResumeSSE(response, httptest.NewRequest(test.method, "/hitl/resume/parent", nil), test.id, false)
			if response.Code < 400 || !strings.Contains(response.Header().Get("Content-Type"), "application/json") || strings.Contains(response.Body.String(), ": heartbeat") {
				t.Fatalf("rejection started streaming: code=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestTerminalResumeCallbackRetainsClarificationAndCompletionOnce(t *testing.T) {
	response := httptest.NewRecorder()
	underlying := NewSSECallback(response, response)
	terminal := &terminalResumeCallback{StreamCallback: underlying}
	terminal.SendUsage(2, 3, 5)
	terminal.SendFinish("stop")
	terminal.SendDone("resumed", []string{"fixture"}, 1, &orchestration.OrchestratorResponse{Clarification: &orchestration.ClarificationRequest{Question: "Which region?"}})
	if response.Body.Len() != 0 {
		t.Fatal("terminal notifications escaped buffering")
	}
	completions := 0
	terminal.deferCompletion(func(err error) {
		if err != nil {
			t.Error(err)
		}
		completions++
	})
	if err := terminal.complete(); err != nil {
		t.Fatal(err)
	}
	terminal.finishCompletion(errors.New("too late"))
	if completions != 1 || !strings.Contains(response.Body.String(), `"clarification":{"question":"Which region?"}`) {
		t.Fatalf("completion lost: calls=%d body=%s", completions, response.Body.String())
	}
	events := parseLikeChatUI(response.Body.String())
	if len(events) != 3 || events[0].typeName != "usage" || events[1].typeName != "finish" || events[2].typeName != "done" {
		t.Fatalf("terminal order = %#v", events)
	}
}
