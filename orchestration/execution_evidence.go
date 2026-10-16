package orchestration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
)

// StepResultFilter selects recorded execution facts, not application outcomes.
// Empty identity fields match any value. Parameters is an exact, type-sensitive
// subset match (no coercion, template resolution, trimming or inference).
type StepResultFilter struct {
	AgentName  string
	Capability string
	Namespace  string
	Parameters map[string]any
}

// LastSuccessfulStep returns the last matching successful, non-skipped step's
// index in the supplied slice, or (-1, false). "Last" means slice order, not
// timestamp order. It does not inspect payloads, attest domain receipts, or
// change the execution's overall success. Callers must not mutate the slice
// concurrently with inspection.
func LastSuccessfulStep(steps []StepResult, filter StepResultFilter) (int, bool) {
	for i := len(steps) - 1; i >= 0; i-- {
		step := &steps[i]
		if !step.Success || step.Skipped ||
			(filter.AgentName != "" && filter.AgentName != step.AgentName) ||
			(filter.Capability != "" && filter.Capability != step.Capability) ||
			(filter.Namespace != "" && filter.Namespace != step.Namespace) {
			continue
		}
		matches := true
		for name, expected := range filter.Parameters {
			actual, exists := step.Parameters[name]
			if !exists || !reflect.DeepEqual(actual, expected) {
				matches = false
				break
			}
		}
		if matches {
			return i, true
		}
	}
	return -1, false
}

// SuccessfulToolData reads a core.ToolResponse envelope from a successful,
// non-skipped step. It reads the final dispatch's raw response when dispatches
// exist and requires that dispatch to have succeeded. For legacy/custom steps
// without dispatches it reads Response, which may already have been normalized
// by the producer. It returns a detached copy of data preserving the selected
// response's bytes, not reconstructing bytes lost before recording.
// Missing/null data, unsuccessful envelopes, unknown envelope fields, duplicate
// JSON keys and multiple JSON values are rejected. Application data fields are
// otherwise opaque; typed decoding and domain validation belong to the caller.
// This is opt-in: arbitrary capability responses need not use ToolResponse.
// On error callers should report the selected result, not silently fall back
// to an older successful response.
func SuccessfulToolData(step StepResult) (json.RawMessage, error) {
	if !step.Success || step.Skipped {
		return nil, errors.New("tool data requires a successful non-skipped step")
	}
	raw := []byte(step.Response)
	if len(step.Dispatches) > 0 {
		dispatch := step.Dispatches[len(step.Dispatches)-1]
		if !dispatch.Success {
			return nil, errors.New("tool data requires a successful final dispatch")
		}
		raw = []byte(dispatch.Response)
	}
	if err := rejectDuplicateJSONFields(raw); err != nil {
		return nil, fmt.Errorf("invalid tool response JSON: %w", err)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("invalid tool response envelope: %w", err)
	}
	for key := range envelope {
		switch key {
		case "success", "data", "error":
		default:
			return nil, fmt.Errorf("unknown tool response envelope field %q", key)
		}
	}
	var success bool
	if value, present := envelope["success"]; present {
		if err := json.Unmarshal(value, &success); err != nil {
			return nil, fmt.Errorf("invalid tool response success flag: %w", err)
		}
	}
	data := envelope["data"]
	if !success || len(data) == 0 || bytes.Equal(data, []byte("null")) {
		return nil, errors.New("tool response contains no successful data")
	}
	if failure := envelope["error"]; len(failure) != 0 && !bytes.Equal(failure, []byte("null")) {
		return nil, errors.New("successful tool response contains an error")
	}
	return data, nil
}

// ExecutionRequestID resolves the execution owner from response/result facts.
// Missing identity is ("", nil); conflicting or non-string recorded identity is
// an error. It never substitutes an incoming parent's request ID or writes to
// context, logs, metadata or tracing baggage.
func ExecutionRequestID(response *OrchestratorResponse, execution *ExecutionResult) (string, error) {
	var responseID, executionID string
	if response != nil {
		responseID = response.RequestID
	}
	if execution != nil {
		if value, present := execution.Metadata[MetadataKeyExecutionRequestID]; present {
			var ok bool
			executionID, ok = value.(string)
			if !ok {
				return "", errors.New("execution request identity is not a string")
			}
		}
	}
	if responseID != "" && executionID != "" && responseID != executionID {
		return "", errors.New("response and execution request identities conflict")
	}
	if responseID != "" {
		return responseID, nil
	}
	return executionID, nil
}
