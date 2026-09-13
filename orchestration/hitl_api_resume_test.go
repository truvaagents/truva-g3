package orchestration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestHITLErrorResponseStableCodes(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"claim lost", &ErrCheckpointResumeClaimLost{}, http.StatusConflict, "resume_claim_lost"},
		{"lifecycle", &ErrCheckpointResumeLifecycle{Stage: "finalize", Cause: errors.New("private cause")}, http.StatusInternalServerError, "resume_failed"},
		{"busy", &ErrCheckpointResumeInProgress{}, http.StatusConflict, "resume_in_progress"},
		{"not resumable", &ErrCheckpointNotResumable{}, http.StatusConflict, "resume_not_resumable"},
		{"invalid input", &ErrInvalidResumeRequest{Field: "private field"}, http.StatusBadRequest, "invalid_resume_request"},
		{"stale decision", &ErrCheckpointStatusConflict{}, http.StatusConflict, "checkpoint_status_conflict"},
		{"owned deletion", &ErrCheckpointDeletionConflict{}, http.StatusConflict, "checkpoint_deletion_conflict"},
		{"unsupported command", &ErrCheckpointCommandUnsupported{CommandType: CommandEdit}, http.StatusBadRequest, "unsupported_command"},
		{"missing", &ErrCheckpointNotFound{}, http.StatusNotFound, "checkpoint_not_found"},
		{"unknown", errors.New("private diagnostic"), http.StatusInternalServerError, "resume_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			status, code, message := HITLErrorResponse(test.err)
			if status != test.status || code != test.code || message == "" || strings.Contains(message, "private") {
				t.Fatalf("mapping = %d %q %q", status, code, message)
			}
			wrappedStatus, wrappedCode, wrappedMessage := HITLErrorResponse(fmt.Errorf("adapter: %w", test.err))
			if wrappedStatus != status || wrappedCode != code || wrappedMessage != message {
				t.Fatal("adapter wrapping changed the public error contract")
			}
		})
	}
}

func TestHITLHandlerOptionalResumeAndInvalidDependencies(t *testing.T) {
	controller, store := newMockInterruptController(), newMockCheckpointStore()
	for _, invalid := range []InterruptController{nil, (*mockInterruptController)(nil)} {
		if _, err := NewHITLHandler(invalid, store); err == nil {
			t.Fatal("nil controller accepted")
		}
	}
	for _, invalid := range []CheckpointPersistence{nil, (*mockCheckpointStore)(nil)} {
		if _, err := NewHITLHandler(controller, invalid); err == nil {
			t.Fatal("nil store accepted")
		}
	}
	for _, invalid := range []HITLHandlerOption{nil, WithHITLResumer(nil), WithHITLResumer((*mockInterruptController)(nil))} {
		if _, err := NewHITLHandler(controller, store, invalid); err == nil {
			t.Fatal("invalid option accepted")
		}
	}
	handler, err := NewHITLHandler(controller, store)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	for _, serve := range []http.HandlerFunc{mux.ServeHTTP, handler.HandleResume} {
		response := httptest.NewRecorder()
		serve(response, httptest.NewRequest(http.MethodPost, "/hitl/resume/parent", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("unconfigured resume = %d", response.Code)
		}
	}
}

func TestHITLResumeHTTPStableOutcomes(t *testing.T) {
	child := &ExecutionCheckpoint{CheckpointID: "child", ParentCheckpointID: "parent", ParentResumeAttemptID: "private", Status: CheckpointStatusPending, ResumeState: &CheckpointResumeState{Owner: "private-owner", AttemptID: "private-attempt"}}
	for _, test := range []struct {
		name   string
		result *ExecutionResult
		err    error
		status int
		code   string
	}{
		{"completed", &ExecutionResult{Success: true}, nil, 200, ""},
		{"nil result", nil, nil, 500, "resume_failed"},
		{"failed result", &ExecutionResult{Success: false}, nil, 500, "resume_failed"},
		{"continued", nil, NewInterruptError(child), 202, ""},
		{"missing child", nil, &ErrInterrupted{CheckpointID: "child"}, 500, "resume_failed"},
		{"busy", nil, &ErrCheckpointResumeInProgress{CheckpointID: "parent"}, 409, "resume_in_progress"},
		{"unapproved", nil, &ErrCheckpointNotResumable{CheckpointID: "parent"}, 409, "resume_not_resumable"},
		{"lost", nil, &ErrCheckpointResumeClaimLost{CheckpointID: "parent"}, 409, "resume_claim_lost"},
		{"missing", nil, &ErrCheckpointNotFound{CheckpointID: "parent"}, 404, "checkpoint_not_found"},
		{"failed finalization with interruption", nil, &ErrCheckpointResumeLifecycle{Stage: "finalize", Cause: errors.New("private backend diagnostic"), ExecutionError: NewInterruptError(child)}, 500, "resume_failed"},
		{"missing successor is not a missing target", nil, &ErrCheckpointResumeLifecycle{Stage: "recovery", Cause: &ErrCheckpointNotFound{CheckpointID: "child"}}, 500, "resume_failed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller, store := newMockInterruptController(), newMockCheckpointStore()
			controller.resumeResult, controller.resumeErr = test.result, test.err
			store.loadErr = errors.New("handler must not do an additional checkpoint read")
			handler := newHITLTestHandler(t, controller, store)
			response := httptest.NewRecorder()
			handler.HandleResume(response, httptest.NewRequest(http.MethodPost, "/hitl/resume/parent", nil))
			if response.Code != test.status {
				t.Fatalf("status = %d: %s", response.Code, response.Body.String())
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if test.code != "" && payload["code"] != test.code {
				t.Fatalf("code = %v", payload)
			}
			if strings.Contains(response.Body.String(), "private") {
				t.Fatalf("ownership/backend detail reached HTTP: %s", response.Body.String())
			}
			if test.status == 202 {
				if payload["interrupted"] != true || payload["checkpoint"].(map[string]interface{})["checkpoint_id"] != "child" {
					t.Fatalf("invalid 202: %v", payload)
				}
			}
		})
	}
}

func TestCheckpointPublicMappingPreservesEveryApplicationField(t *testing.T) {
	checkpoint := &ExecutionCheckpoint{
		CheckpointID: "cp", RequestID: "request", OriginalRequestID: "root", OriginalTraceID: "trace", OriginalSpanID: "span",
		AgentName: "agent", AgentAddress: "http://agent", InterruptPoint: InterruptPointBeforeStep,
		Decision: &InterruptDecision{Message: "owner and token are application words"}, Plan: &RoutingPlan{PlanID: "plan"},
		CompletedSteps: []StepResult{{StepID: "completed"}}, CurrentStep: &RoutingStep{StepID: "current"}, CurrentStepResult: &StepResult{StepID: "current-result"},
		StepResults: map[string]*StepResult{"first": {Response: "original result"}}, ResolvedParameters: map[string]interface{}{"token": "literal"},
		OriginalRequest: "exact request", UserContext: map[string]interface{}{"owner": "application-owner", "attempt_id": "application-attempt"},
		RequestMode: RequestModeStreaming, PhaseNumber: 4, AccumulatedResults: map[string]*StepResult{"earlier": {Success: true}},
		ExecutedStepIDs: []string{"earlier"}, ContinuationNote: "continue", SkillState: &SkillExecutionState{}, SkillCacheContext: &SkillCacheContext{},
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), Status: CheckpointStatusContinued,
		ParentCheckpointID: "previous", ParentResumeAttemptID: "internal-attempt",
		ResumeState: &CheckpointResumeState{AttemptID: "internal-attempt", Owner: "internal-owner", ReservedSuccessorCheckpointID: "reservation", SuccessorCheckpointID: "successor"},
	}
	public := CheckpointResponseFrom(checkpoint)
	internalValue, publicValue := reflect.ValueOf(checkpoint).Elem(), reflect.ValueOf(public).Elem()
	for i := 0; i < internalValue.NumField(); i++ {
		name := internalValue.Type().Field(i).Name
		if name == "ResumeState" || name == "ParentResumeAttemptID" {
			continue
		}
		mapped := publicValue.FieldByName(name)
		if !mapped.IsValid() || !reflect.DeepEqual(mapped.Interface(), internalValue.Field(i).Interface()) {
			t.Fatalf("public mapping lost field %s", name)
		}
	}
	if public.SuccessorCheckpointID != "successor" || CheckpointResponseFrom(nil) != nil {
		t.Fatal("public lineage mapping wrong")
	}
	encoded, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("internal-")) || bytes.Contains(encoded, []byte("reservation")) || bytes.Contains(encoded, []byte("resume_state")) {
		t.Fatalf("internal ownership reached DTO: %s", encoded)
	}
	if !bytes.Contains(encoded, []byte("application-owner")) || !bytes.Contains(encoded, []byte("literal")) {
		t.Fatal("application content was altered")
	}
	persisted, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(persisted, []byte("internal-owner")) || !bytes.Contains(persisted, []byte("resume_state")) {
		t.Fatal("ownership was lost from persistence")
	}
	checkpoint.Status = CheckpointStatusResuming
	if CheckpointResponseFrom(checkpoint).SuccessorCheckpointID != "" {
		t.Fatal("unfinalized reservation exposed as successor")
	}
}

func TestHITLUnsupportedCommandsRejectBeforeReadOrMutation(t *testing.T) {
	for _, command := range []CommandType{CommandEdit, CommandSkip, CommandRetry, CommandRespond} {
		store := newMockCheckpointStore()
		store.loadErr = errors.New("must not read")
		controller := newMockInterruptController()
		handler := newHITLTestHandler(t, controller, store)
		body, err := json.Marshal(Command{CheckpointID: "parent", Type: command})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		handler.HandleCommand(response, httptest.NewRequest(http.MethodPost, "/hitl/command", bytes.NewReader(body)))
		if response.Code != http.StatusBadRequest || controller.lastCommand != nil || !strings.Contains(response.Body.String(), "unsupported_command") {
			t.Fatalf("unsupported command reached execution: %s", response.Body.String())
		}
	}
}

func TestHITLResumePreservesRequestCancellation(t *testing.T) {
	store := &resumeStoreStub{claim: func(ctx context.Context, _ ResumeClaimRequest) (*CheckpointResumeClaim, error) { return nil, ctx.Err() }}
	r := newResumeTestCoordinator(t, store, func(context.Context, *ExecutionCheckpoint) (*ExecutionResult, error) {
		t.Fatal("canceled request executed")
		return nil, nil
	})
	handler, err := NewHITLHandler(newMockInterruptController(), newMockCheckpointStore(), WithHITLResumer(r))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request := httptest.NewRequest(http.MethodPost, "/hitl/resume/parent", nil).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.HandleResume(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("canceled request returned success: %d", response.Code)
	}
}
