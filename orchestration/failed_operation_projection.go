package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// FailedOperationIdentity identifies one actual failed dispatch. It is not an
// authorization grant; applications own disclosure across delegation boundaries.
type FailedOperationIdentity struct {
	StepID     string
	AgentName  string
	Capability string
	Attempt    int
}

// FailedOperationInputPolicy may restrict which actual inputs reach a derived
// prompt/delegation view. It cannot replace their values. An error omits all
// inputs for that attempt. Source execution records remain unchanged.
type FailedOperationInputPolicy interface {
	AllowFailedOperationInput(context.Context, FailedOperationIdentity, string, interface{}) (bool, error)
}

// FailedOperationViewConfig configures bounded failure evidence. Zero numeric
// code values mean unset; negative values are invalid. Numeric environment
// overrides must be positive. Disabled is independent of numeric limits.
type FailedOperationViewConfig struct {
	Disabled       bool                       `json:"disabled,omitempty"`
	MaxValueBytes  int                        `json:"max_value_bytes,omitempty"`
	MaxStepBytes   int                        `json:"max_step_bytes,omitempty"`
	MaxPromptBytes int                        `json:"max_prompt_bytes,omitempty"`
	InputPolicy    FailedOperationInputPolicy `json:"-"`
}

// WithFailedOperationView replaces the derived-view configuration. Resolve the
// options through ResolveOrchestratorConfig so invalid limits fail construction.
// An application policy is immutable for the orchestrator's lifetime and must
// support concurrent calls. It makes semantic cache identity ineligible.
func WithFailedOperationView(config FailedOperationViewConfig) OrchestratorOption {
	return func(target *OrchestratorConfig) { target.FailedOperationView = config }
}

func resolveFailedOperationView(c FailedOperationViewConfig) (FailedOperationViewConfig, error) {
	for _, field := range []struct {
		name     string
		value    *int
		fallback int
	}{
		{"max_value_bytes", &c.MaxValueBytes, 4096}, {"max_step_bytes", &c.MaxStepBytes, 16384}, {"max_prompt_bytes", &c.MaxPromptBytes, 32768},
	} {
		if *field.value < 0 {
			return c, fmt.Errorf("failed_operation_view.%s must be positive or unset", field.name)
		}
		if *field.value == 0 {
			*field.value = field.fallback
		}
	}
	return c, nil
}

type failedAttemptEvidence struct {
	Attempt         int                    `json:"attempt"`
	ErrorCode       string                 `json:"error_code,omitempty"`
	CodeUnavailable bool                   `json:"code_unavailable,omitempty"`
	Inputs          map[string]interface{} `json:"attempted_inputs,omitempty"`
	OmittedInputs   int                    `json:"omitted_inputs"`
	PolicyError     bool                   `json:"policy_error,omitempty"`
}

type failedOperationEvidence struct {
	StepID          string                  `json:"step_id"`
	AgentName       string                  `json:"agent"`
	Capability      string                  `json:"capability,omitempty"`
	DispatchState   string                  `json:"dispatch_state"`
	Error           string                  `json:"error,omitempty"`
	StructuredError json.RawMessage         `json:"structured_error,omitempty"`
	Attempts        []failedAttemptEvidence `json:"attempts,omitempty"`
	OmittedAttempts int                     `json:"omitted_attempts"`
	ViewStatus      string                  `json:"view_status"`
}

type preparedFailedOperation struct{ raw, prepared string }

// ProjectFailedOperationEvidence is an explicit application composition seam
// for delegation. It returns bounded JSON views of failed steps, never planned
// inputs. Calling it authorizes the selected view within the application's own
// boundary; the framework does not automatically send it to another agent.
// An empty value means the step's view was omitted by the configured budget.
func ProjectFailedOperationEvidence(ctx context.Context, steps []StepResult, config FailedOperationViewConfig) (map[string]json.RawMessage, error) {
	views, err := projectFailedOperations(ctx, steps, config, -1, func(s string) (string, error) { return s, nil })
	if err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(views))
	for id, v := range views {
		if v.raw != "" {
			out[id] = json.RawMessage(v.raw)
		} else {
			out[id] = nil
		}
	}
	return out, nil
}

func projectFailedOperations(ctx context.Context, steps []StepResult, config FailedOperationViewConfig, parentShare int, encode func(string) (string, error)) (map[string]preparedFailedOperation, error) {
	config, err := resolveFailedOperationView(config)
	if err != nil {
		return nil, err
	}
	if config.Disabled {
		return nil, nil
	}
	out := make(map[string]preparedFailedOperation)
	remaining := config.MaxPromptBytes
	// Newest failed steps get the bounded view first. Every failure still keeps
	// its ordinary framework marker even when no JSON view fits.
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step.Success {
			continue
		}
		budget := min(config.MaxStepBytes, remaining)
		if parentShare >= 0 {
			budget = min(budget, parentShare)
		}
		view, err := projectFailedOperation(ctx, step, config, budget, encode)
		if err != nil {
			return nil, err
		}
		out[step.StepID] = view
		remaining -= len(view.prepared)
		telemetry.AddSpanEvent(ctx, "failure_evidence.projected", attribute.String("request_id", GetRequestID(ctx)), attribute.String("step_id", step.StepID), attribute.Int("effective_limit_bytes", budget), attribute.Int("raw_bytes", len(view.raw)), attribute.Int("rendered_bytes", len(view.prepared)), attribute.Bool("omitted", view.raw == ""))
	}
	return out, nil
}

func projectFailedOperation(ctx context.Context, step StepResult, config FailedOperationViewConfig, budget int, encode func(string) (string, error)) (preparedFailedOperation, error) {
	view := failedOperationEvidence{StepID: step.StepID, AgentName: step.AgentName, Capability: step.Capability, DispatchState: "unavailable", ViewStatus: "complete"}
	if kind, _ := step.Metadata["failure_kind"].(string); kind == "binding_shape_mismatch" {
		view.DispatchState = "not_dispatched"
	}
	for _, dispatch := range step.Dispatches {
		if !dispatch.Success {
			view.OmittedAttempts++
		}
	}
	if view.OmittedAttempts > 0 {
		view.DispatchState = "dispatched"
	}
	// ViewStatus describes projection coverage only; it is never a task verdict.
	view.ViewStatus = "partial"
	var last preparedFailedOperation
	fit := func() (bool, error) {
		raw, err := json.Marshal(view)
		if err != nil {
			return false, err
		}
		prepared, err := encode(string(raw))
		if err != nil {
			return false, err
		}
		if len(prepared) > budget {
			return false, nil
		}
		last = preparedFailedOperation{string(raw), prepared}
		return true, nil
	}
	if ok, err := fit(); err != nil || !ok {
		return last, err
	}
	// Preserve existing terminal error text as a separate source, not a code
	// assigned to an earlier dispatch. Oversized details are explicitly partial.
	if len(step.Error) <= min(config.MaxValueBytes, budget/4) {
		view.Error = step.Error
		if ok, err := fit(); err != nil {
			return last, err
		} else if !ok {
			view.Error = ""
		}
	}
	if len(step.ToolError) > 0 && len(step.ToolError) <= min(config.MaxValueBytes, budget/4) && json.Valid(step.ToolError) {
		view.StructuredError = append(json.RawMessage(nil), step.ToolError...)
		if ok, err := fit(); err != nil {
			return last, err
		} else if !ok {
			view.StructuredError = nil
		}
	}
	for i := len(step.Dispatches) - 1; i >= 0; i-- {
		dispatch := step.Dispatches[i]
		if dispatch.Success {
			continue
		}
		attempt := failedAttemptEvidence{Attempt: dispatch.Number, OmittedInputs: len(dispatch.Parameters)}
		var code struct {
			Code string `json:"code"`
		}
		if json.Unmarshal(componentError(dispatch.Response), &code) == nil && len(code.Code) <= config.MaxValueBytes {
			attempt.ErrorCode = code.Code
		}
		attempt.CodeUnavailable = attempt.ErrorCode == ""
		identity := FailedOperationIdentity{step.StepID, step.AgentName, step.Capability, dispatch.Number}
		keys := make([]string, 0, len(dispatch.Parameters))
		for key := range dispatch.Parameters {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		allowed := make([]string, 0, len(keys))
		for _, key := range keys {
			allow := true
			if config.InputPolicy != nil {
				// Isolate typed maps/slices too, not only generic decoded JSON.
				data, err := json.Marshal(dispatch.Parameters[key])
				if err != nil {
					continue
				}
				isolated, err := unmarshalPreservingNumbers(data)
				if err != nil {
					continue
				}
				allow, err = config.InputPolicy.AllowFailedOperationInput(ctx, identity, key, isolated)
				if err != nil {
					attempt.PolicyError = true
					allowed = nil
					telemetry.AddSpanEvent(ctx, "failure_evidence.policy_omitted", attribute.String("request_id", GetRequestID(ctx)), attribute.String("step_id", step.StepID), attribute.Int("attempt", dispatch.Number))
					break
				}
			}
			if allow {
				allowed = append(allowed, key)
			}
		}
		view.Attempts = append(view.Attempts, attempt)
		view.OmittedAttempts--
		if ok, err := fit(); err != nil {
			return last, err
		} else if !ok {
			view.Attempts = view.Attempts[:len(view.Attempts)-1]
			view.OmittedAttempts++
			break
		}
		index := len(view.Attempts) - 1
		view.Attempts[index].Inputs = make(map[string]interface{})
		for _, key := range allowed {
			value := cloneGeneratedValue(dispatch.Parameters[key])
			data, err := json.Marshal(value)
			if err != nil {
				continue
			}
			encodedValue, err := encode(string(data))
			if err != nil {
				return last, err
			}
			if len(encodedValue) > config.MaxValueBytes {
				continue
			}
			view.Attempts[index].Inputs[key] = value
			view.Attempts[index].OmittedInputs--
			if ok, err := fit(); err != nil {
				return last, err
			} else if !ok {
				delete(view.Attempts[index].Inputs, key)
				view.Attempts[index].OmittedInputs++
			}
		}
	}
	complete := view.OmittedAttempts == 0 && view.Error == step.Error && (len(step.ToolError) == 0 || len(view.StructuredError) > 0)
	for _, attempt := range view.Attempts {
		complete = complete && attempt.OmittedInputs == 0 && !attempt.PolicyError
	}
	if complete {
		view.ViewStatus = "complete"
		if _, err := fit(); err != nil {
			return last, err
		}
	}
	return last, nil
}

func synthesisFailureViews(ctx context.Context, steps []StepResult, config FailedOperationViewConfig) (map[string]preparedFailedOperation, error) {
	return projectFailedOperations(ctx, steps, config, -1, func(raw string) (string, error) {
		return preparePromptValue(ctx, promptSynthesis, promptValuePriorResult, promptFieldPriorResultError, raw)
	})
}

func prepareFailedSynthesisStep(ctx context.Context, step StepResult, views map[string]preparedFailedOperation) (string, string, string, error) {
	view, ok := views[step.StepID]
	if !ok {
		return prepareSynthesisStepValues(ctx, step.AgentName, step.Instruction, step.Error, promptFieldPriorResultError)
	}
	content := view.prepared
	if content == "" {
		content = "[FAILED: evidence view omitted for budget; original execution record retained]"
	}
	return prepareSynthesisStepValues(ctx, step.AgentName, step.Instruction, content, promptFieldPriorResultError, true)
}

// Mirrors the existing soft retention rule without changing it. This baseline
// exists only to expose successful evidence displaced by the added failure view.
func continuationSuccessfulRetention(steps []StepResult, bodies []string, spent, budget int) []string {
	var out []string
	for i := len(steps) - 1; i >= 0; i-- {
		if !steps[i].Success {
			continue
		}
		cost := stepRenderCost(&steps[i], bodies[i])
		if len(out) > 0 && spent+cost > budget {
			break
		}
		out = append(out, steps[i].StepID)
		spent += cost
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}
