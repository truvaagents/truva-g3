package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

// planEnvelopeError describes model wire intent only. Its Error is body-free;
// key names are untrusted data exposed only by the bounded correction view.
type planEnvelopeError struct {
	reason string
	shape  string
	keys   []string
}

func (err *planEnvelopeError) Error() string { return err.reason }

func decodePlanEnvelope(raw []byte) (*RoutingPlan, error) {
	var plan RoutingPlan
	// Preserve the existing typed errors for malformed known fields and roots.
	if err := json.Unmarshal(raw, &plan); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, &planEnvelopeError{reason: "unrecognized_plan_envelope", shape: "null"}
	}
	invalidClarification := plan.NeedsUserInput != nil && strings.TrimSpace(plan.NeedsUserInput.Question) == ""
	if !invalidClarification && (len(plan.Steps) > 0 || plan.NeedsUserInput != nil || (plan.Terminal != nil && *plan.Terminal)) {
		return &plan, nil
	}
	err := &planEnvelopeError{reason: "unrecognized_plan_envelope", shape: "object"}
	if invalidClarification {
		// Clarification short-circuits even when steps or terminal intent are
		// present. A fresh model response must supply an actual question.
		err.reason = "invalid_clarification_request"
	}
	for key := range fields {
		err.keys = append(err.keys, key)
		if !invalidClarification && (key == "steps" || key == "terminal" || key == "needs_user_input") {
			err.reason = "missing_explicit_plan_intent"
		}
	}
	sort.Strings(err.keys)
	return nil, err
}

func planParseReason(err error) string {
	var envelope *planEnvelopeError
	if errors.As(err, &envelope) {
		return envelope.reason
	}
	return "plan_decode_failed"
}

// buildPlanEnvelopeFeedback charges the complete encoded diagnostic and guidance
// to the existing correction budget. It never includes envelope field values.
func (o *AIOrchestrator) buildPlanEnvelopeFeedback(ctx context.Context, envelope *planEnvelopeError) (string, error) {
	limit := defaultRegenerationFeedbackMaxBytes
	if o.config != nil && o.config.IterativePlanning.RegenerationFeedbackMaxBytes > 0 {
		limit = o.config.IterativePlanning.RegenerationFeedbackMaxBytes
	}
	return buildPlanEnvelopeFeedbackWithin(ctx, envelope, limit)
}

func buildPlanEnvelopeFeedbackWithin(ctx context.Context, envelope *planEnvelopeError, limit int) (string, error) {
	const prefix = "\n<parse_error>\nThe decoded JSON does not satisfy the routing-plan intent contract.\nEnvelope diagnostic (quoted JSON data): "
	const suffix = "\n</parse_error>\nReturn the documented routing-plan object directly. Use nonempty steps for intended work, terminal:true for finished/no-further-action intent, or needs_user_input with a nonblank question for clarification. Preserve literal string contents using JSON escaping.\n"
	data := struct {
		Reason      string   `json:"reason"`
		Shape       string   `json:"shape"`
		FoundKeys   []string `json:"found_top_level_keys"`
		OmittedKeys int      `json:"omitted_keys"`
	}{Reason: envelope.reason, Shape: envelope.shape, FoundKeys: []string{}, OmittedKeys: len(envelope.keys)}
	encode := func() (string, error) {
		raw, err := json.Marshal(data)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	fits := func(value string) bool {
		quoted, _ := quoteCorrectionValue(value) // strings always JSON-encode.
		return len(prefix)+len(quoted)+len(suffix) <= limit
	}
	encoded, err := encode()
	if err != nil {
		return "", err
	}
	if !fits(encoded) {
		return "", errRegenerationContextTooLarge
	}
	for _, key := range envelope.keys {
		data.FoundKeys = append(data.FoundKeys, key)
		data.OmittedKeys--
		candidate, encodeErr := encode()
		if encodeErr != nil {
			return "", encodeErr
		}
		if !fits(candidate) {
			break // encoded still represents the previous complete admission.
		}
		encoded = candidate
	}
	prepared, err := prepareValidationFeedback(ctx, promptRegeneration, errors.New(encoded))
	if err != nil {
		return "", err
	}
	// Quote after the application preparation boundary as well: a custom
	// preparer may return text containing reserved section delimiters.
	prepared, err = quoteCorrectionValue(prepared)
	if err != nil {
		return "", err
	}
	if len(prefix)+len(prepared)+len(suffix) > limit {
		return "", errRegenerationContextTooLarge
	}
	return prefix + prepared + suffix, nil
}
