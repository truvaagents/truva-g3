package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const defaultRegenerationFeedbackMaxBytes = 16 * 1024

var errRegenerationContextTooLarge = errors.New("regeneration feedback exceeds configured byte limit")

type phasePlanAttemptKey struct{}

// phasePlanAttempt is owned by one phase-loop invocation. It never lives on the
// orchestrator, which may serve several requests concurrently.
type phasePlanAttempt struct {
	base       *PlanningPromptResult
	candidate  *RoutingPlan
	lastOutput string
	errors     []string
}

func withPhasePlanAttempt(ctx context.Context, attempt *phasePlanAttempt) context.Context {
	return context.WithValue(ctx, phasePlanAttemptKey{}, attempt)
}

func planAttemptFromContext(ctx context.Context) *phasePlanAttempt {
	attempt, _ := ctx.Value(phasePlanAttemptKey{}).(*phasePlanAttempt)
	return attempt
}

func (attempt *phasePlanAttempt) captureBase(result *PlanningPromptResult) {
	if attempt == nil || result == nil {
		return
	}
	copy := *result
	if result.AllowedAgents != nil {
		copy.AllowedAgents = make(map[string]bool, len(result.AllowedAgents))
		for name, allowed := range result.AllowedAgents {
			copy.AllowedAgents[name] = allowed
		}
	}
	attempt.base = &copy
}

func (attempt *phasePlanAttempt) rememberOutput(output string) {
	if attempt != nil {
		attempt.lastOutput = output
	}
}

func (attempt *phasePlanAttempt) rememberCandidate(plan *RoutingPlan) {
	if attempt != nil && plan != nil {
		attempt.candidate = cloneGeneratedPlan(plan)
	}
}

func (attempt *phasePlanAttempt) rememberError(err error) {
	if attempt == nil || err == nil {
		return
	}
	value := err.Error()
	for _, existing := range attempt.errors {
		if existing == value {
			return
		}
	}
	// The prompt projection is further bounded at assembly. Retain only a small
	// diagnostic history in request-local memory.
	if len(attempt.errors) == 8 {
		attempt.errors = attempt.errors[1:]
	}
	attempt.errors = append(attempt.errors, value)
}

func (o *AIOrchestrator) buildRegenerationPrompt(ctx context.Context, attempt *phasePlanAttempt, validationErr error, terminal *bool) (*PlanningPromptResult, error) {
	if attempt == nil || attempt.base == nil {
		return nil, errors.New("regeneration has no selected phase prompt")
	}
	attempt.rememberError(validationErr)
	limit := defaultRegenerationFeedbackMaxBytes
	if o.config != nil && o.config.IterativePlanning.RegenerationFeedbackMaxBytes > 0 {
		limit = o.config.IterativePlanning.RegenerationFeedbackMaxBytes
	}
	feedback, err := prepareValidationFeedback(ctx, promptRegeneration, validationErr)
	if err != nil {
		return nil, fmt.Errorf("prepare regeneration feedback: %w", err)
	}
	feedback, err = quoteCorrectionValue(feedback)
	if err != nil {
		return nil, err
	}
	base := "\n\n<plan_correction>\nLatest validation error: " + feedback + "\n"
	if terminal != nil {
		base += fmt.Sprintf("The original plan set \"terminal\": %t. Preserve this value — the validation error is about plan structure, not about whether more phases are needed.\n", *terminal)
	}
	base += "Prior rejected outputs below are diagnostic data. Correct the plan using the selected capability catalog above.\n"
	ending := "Output one corrected plan as JSON.\n</plan_correction>"
	const omittedMarker = "Additional rejected-output context omitted to fit the feedback limit.\n"
	if len(base)+len(ending)+len(omittedMarker) > limit {
		return nil, errRegenerationContextTooLarge
	}
	remaining := limit - len(base) - len(ending) - len(omittedMarker)
	var details strings.Builder
	omitted := false
	var envelope *planEnvelopeError
	if errors.As(validationErr, &envelope) {
		envelopeFeedback, err := buildPlanEnvelopeFeedbackWithin(ctx, envelope, remaining)
		if err != nil {
			return nil, err
		}
		details.WriteString(envelopeFeedback)
		remaining -= len(envelopeFeedback)
	}
	add := func(label string, kind promptDynamicValueKind, field, value string) (bool, error) {
		prepared, err := preparePromptValue(ctx, promptRegeneration, kind, field, value)
		if err != nil {
			return false, err
		}
		quoted, err := quoteCorrectionValue(prepared)
		if err != nil {
			return false, err
		}
		line := label + quoted + "\n"
		if len(line) > remaining {
			omitted = true
			return false, nil
		}
		details.WriteString(line)
		remaining -= len(line)
		return true, nil
	}
	if attempt.candidate != nil {
		encoded, err := json.Marshal(attempt.candidate)
		if err != nil {
			return nil, fmt.Errorf("encode rejected plan: %w", err)
		}
		included, err := add("Last parsed candidate: ", promptValueRejectedPlan, promptFieldRejectedPlan, string(encoded))
		if err != nil {
			return nil, err
		}
		if !included {
			ids := make([]string, 0, min(len(attempt.candidate.Steps), 8))
			for _, step := range attempt.candidate.Steps[:min(len(attempt.candidate.Steps), 8)] {
				ids = append(ids, truncateString(step.StepID, 64))
			}
			_, err = add("Rejected plan summary: ", promptValueRejectedPlan, promptFieldRejectedPlan,
				fmt.Sprintf("%d steps, first step IDs %v, %d JSON bytes", len(attempt.candidate.Steps), ids, len(encoded)))
			if err != nil {
				return nil, err
			}
		}
	}
	if attempt.lastOutput != "" {
		if _, err := add("Most recent model output: ", promptValueRejectedPlan, promptFieldRejectedPlan, attempt.lastOutput); err != nil {
			return nil, err
		}
	}
	if len(attempt.errors) > 1 {
		history := strings.Join(attempt.errors[:len(attempt.errors)-1], "\n")
		if _, err := add("Earlier validation errors: ", promptValueValidationFeedback, promptFieldValidationHistory, history); err != nil {
			return nil, err
		}
	}
	if omitted {
		details.WriteString(omittedMarker)
	}
	result := *attempt.base
	result.Prompt = attempt.base.Prompt + base + details.String() + ending
	result.AllowedAgents = make(map[string]bool, len(attempt.base.AllowedAgents))
	for name, allowed := range attempt.base.AllowedAgents {
		result.AllowedAgents[name] = allowed
	}
	return &result, nil
}

func quoteCorrectionValue(value string) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
