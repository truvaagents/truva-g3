package orchestration

import (
	"errors"
	"fmt"
)

var (
	errAfterPlanningCloneFailed       = errors.New("after-planning plan clone failed")
	errAfterPlanningInvalidResultType = errors.New("after-planning hook returned an invalid plan type")
	errAfterPlanningInvalidPlan       = errors.New("after-planning hook returned an invalid plan")
)

// RequiredAfterPlanningError reports that a mandatory after-planning hook
// could not produce a valid governed plan. The prior plan is not returned to
// the lifecycle and no downstream HITL or execution stage may run.
type RequiredAfterPlanningError struct {
	HookName string
	Cause    error
}

// Error implements error.
func (e *RequiredAfterPlanningError) Error() string {
	if e == nil {
		return "required after-planning hook failed"
	}
	if e.Cause == nil {
		return fmt.Sprintf("required after-planning hook %q failed", e.HookName)
	}
	return fmt.Sprintf("required after-planning hook %q failed: %v", e.HookName, e.Cause)
}

// Unwrap returns the hook failure that caused the required boundary to fail.
func (e *RequiredAfterPlanningError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// IsRequiredAfterPlanningError reports whether err contains a required
// after-planning runtime failure.
func IsRequiredAfterPlanningError(err error) bool {
	var target *RequiredAfterPlanningError
	return errors.As(err, &target)
}

// InvalidRequiredAfterPlanningHookReason identifies an invalid required-hook
// registration using a bounded, stable value.
type InvalidRequiredAfterPlanningHookReason string

const (
	// InvalidRequiredAfterPlanningStageDrift means a required marker no longer
	// implements the AfterPlanningHook stage contract.
	InvalidRequiredAfterPlanningStageDrift InvalidRequiredAfterPlanningHookReason = "stage_drift"
	// InvalidRequiredAfterPlanningMultiple means more than one hook declares
	// the required after-planning boundary.
	InvalidRequiredAfterPlanningMultiple InvalidRequiredAfterPlanningHookReason = "multiple_required"
	// InvalidRequiredAfterPlanningNotLast means a later registered hook can
	// still mutate the plan after the required boundary.
	InvalidRequiredAfterPlanningNotLast InvalidRequiredAfterPlanningHookReason = "required_not_terminal"
)

// InvalidRequiredAfterPlanningHookError reports invalid required-hook
// composition discovered before the orchestrator starts serving requests.
type InvalidRequiredAfterPlanningHookError struct {
	HookName string
	Reason   InvalidRequiredAfterPlanningHookReason
}

// Error implements error.
func (e *InvalidRequiredAfterPlanningHookError) Error() string {
	if e == nil {
		return "invalid required after-planning hook"
	}
	return fmt.Sprintf("invalid required after-planning hook %q: %s", e.HookName, e.Reason)
}

// Unwrap classifies invalid required-hook composition as invalid explicit
// orchestrator configuration while preserving its more specific typed error.
func (e *InvalidRequiredAfterPlanningHookError) Unwrap() error {
	if e == nil {
		return nil
	}
	return ErrInvalidOrchestratorConfig
}

// IsInvalidRequiredAfterPlanningHook reports whether err contains invalid
// required-hook construction configuration.
func IsInvalidRequiredAfterPlanningHook(err error) bool {
	var target *InvalidRequiredAfterPlanningHookError
	return errors.As(err, &target)
}
