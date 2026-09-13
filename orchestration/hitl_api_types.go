package orchestration

import (
	"errors"
	"net/http"
	"time"
)

// CheckpointResponse is the public checkpoint protocol. Attempt ownership stays
// in persistence and internal observability. Application fields are not scanned
// or altered; this is an explicit framework field mapping, not redaction.
type CheckpointResponse struct {
	CheckpointID          string                 `json:"checkpoint_id"`
	RequestID             string                 `json:"request_id"`
	OriginalRequestID     string                 `json:"original_request_id,omitempty"`
	OriginalTraceID       string                 `json:"original_trace_id,omitempty"`
	OriginalSpanID        string                 `json:"original_span_id,omitempty"`
	AgentName             string                 `json:"agent_name,omitempty"`
	AgentAddress          string                 `json:"agent_address,omitempty"`
	InterruptPoint        InterruptPoint         `json:"interrupt_point"`
	Decision              *InterruptDecision     `json:"decision"`
	Plan                  *RoutingPlan           `json:"plan"`
	CompletedSteps        []StepResult           `json:"completed_steps"`
	CurrentStep           *RoutingStep           `json:"current_step,omitempty"`
	CurrentStepResult     *StepResult            `json:"current_step_result,omitempty"`
	StepResults           map[string]*StepResult `json:"step_results"`
	ResolvedParameters    map[string]interface{} `json:"resolved_parameters,omitempty"`
	OriginalRequest       string                 `json:"original_request"`
	UserContext           map[string]interface{} `json:"user_context,omitempty"`
	RequestMode           RequestMode            `json:"request_mode,omitempty"`
	PhaseNumber           int                    `json:"phase_number,omitempty"`
	AccumulatedResults    map[string]*StepResult `json:"accumulated_results,omitempty"`
	ExecutedStepIDs       []string               `json:"executed_step_ids,omitempty"`
	ContinuationNote      string                 `json:"continuation_note_checkpoint,omitempty"`
	SkillState            *SkillExecutionState   `json:"skill_state,omitempty"`
	SkillCacheContext     *SkillCacheContext     `json:"skill_cache_context,omitempty"`
	CreatedAt             time.Time              `json:"created_at"`
	ExpiresAt             time.Time              `json:"expires_at"`
	Status                CheckpointStatus       `json:"status"`
	ParentCheckpointID    string                 `json:"parent_checkpoint_id,omitempty"`
	SuccessorCheckpointID string                 `json:"successor_checkpoint_id,omitempty"`
}

// CheckpointResponseFrom allocates a public representation without modifying the
// checkpoint. Nested application values are preserved for serialization.
func CheckpointResponseFrom(cp *ExecutionCheckpoint) *CheckpointResponse {
	if cp == nil {
		return nil
	}
	response := &CheckpointResponse{
		CheckpointID: cp.CheckpointID, RequestID: cp.RequestID,
		OriginalRequestID: cp.OriginalRequestID, OriginalTraceID: cp.OriginalTraceID, OriginalSpanID: cp.OriginalSpanID,
		AgentName: cp.AgentName, AgentAddress: cp.AgentAddress,
		InterruptPoint: cp.InterruptPoint, Decision: cp.Decision, Plan: cp.Plan,
		CompletedSteps: cp.CompletedSteps, CurrentStep: cp.CurrentStep, CurrentStepResult: cp.CurrentStepResult, StepResults: cp.StepResults,
		ResolvedParameters: cp.ResolvedParameters, OriginalRequest: cp.OriginalRequest, UserContext: cp.UserContext,
		RequestMode: cp.RequestMode, PhaseNumber: cp.PhaseNumber, AccumulatedResults: cp.AccumulatedResults,
		ExecutedStepIDs: cp.ExecutedStepIDs, ContinuationNote: cp.ContinuationNote,
		SkillState: cp.SkillState, SkillCacheContext: cp.SkillCacheContext,
		CreatedAt: cp.CreatedAt, ExpiresAt: cp.ExpiresAt, Status: cp.Status, ParentCheckpointID: cp.ParentCheckpointID,
	}
	if cp.Status == CheckpointStatusContinued && cp.ResumeState != nil {
		response.SuccessorCheckpointID = cp.ResumeState.SuccessorCheckpointID
	}
	return response
}

type ResumeInterruptedResponse struct {
	Interrupted bool                `json:"interrupted"`
	Checkpoint  *CheckpointResponse `json:"checkpoint"`
}

// HITLErrorResponse maps framework lifecycle errors to stable HTTP/SSE codes.
// No automatic retry is implied: claim loss and post-claim failure can follow
// external effects. Error causes remain available to the application's caller.
func HITLErrorResponse(err error) (status int, code, message string) {
	var lost *ErrCheckpointResumeClaimLost
	var lifecycle *ErrCheckpointResumeLifecycle
	var busy *ErrCheckpointResumeInProgress
	var notResumable *ErrCheckpointNotResumable
	var invalid *ErrInvalidResumeRequest
	var statusConflict *ErrCheckpointStatusConflict
	var deleteConflict *ErrCheckpointDeletionConflict
	var unsupported *ErrCheckpointCommandUnsupported
	switch {
	case errors.As(err, &lost):
		return http.StatusConflict, "resume_claim_lost", "resume ownership was lost; work may have executed"
	case errors.As(err, &lifecycle):
		return http.StatusInternalServerError, "resume_failed", "resume did not complete; work may have executed"
	case errors.As(err, &busy):
		return http.StatusConflict, "resume_in_progress", "checkpoint is owned by another resume attempt"
	case errors.As(err, &notResumable):
		return http.StatusConflict, "resume_not_resumable", "checkpoint is not approved for resumption"
	case errors.As(err, &invalid):
		return http.StatusBadRequest, "invalid_resume_request", "invalid resume request"
	case errors.As(err, &statusConflict):
		return http.StatusConflict, "checkpoint_status_conflict", "checkpoint status changed; reload before deciding"
	case errors.As(err, &deleteConflict):
		return http.StatusConflict, "checkpoint_deletion_conflict", "an owned checkpoint cannot be deleted"
	case errors.As(err, &unsupported):
		return http.StatusBadRequest, "unsupported_command", "only approve, reject, and abort commands are supported"
	case IsCheckpointNotFound(err):
		return http.StatusNotFound, "checkpoint_not_found", "checkpoint was not found"
	default:
		return http.StatusInternalServerError, "resume_failed", "resume did not complete; inspect execution evidence before retrying"
	}
}

func (h *HITLHandler) writeHITLError(w http.ResponseWriter, status int, code, message string) {
	h.writeJSON(w, status, &ErrorResponse{Code: code, Error: message})
}
