package openaiwire

import (
	"errors"
	"fmt"
	"strings"
)

const profileVersion = "profile-v2"

// ModelFieldMode controls whether the OpenAI-compatible body carries a model.
type ModelFieldMode uint8

const (
	// ModelFieldRequired protects a nonempty wire model in the request body.
	ModelFieldRequired ModelFieldMode = iota + 1
	// ModelFieldOmitted protects the intentional absence of a body model.
	ModelFieldOmitted
)

// TokenLimitField selects the supported OpenAI-compatible token-limit spelling.
type TokenLimitField uint8

const (
	// TokenLimitMaxTokens emits max_tokens.
	TokenLimitMaxTokens TokenLimitField = iota + 1
	// TokenLimitMaxCompletionTokens emits max_completion_tokens.
	TokenLimitMaxCompletionTokens
)

// TokenBudgetPolicy controls whether the portable max-token budget is sent
// exactly or expanded to reserve tokens for active hidden reasoning.
type TokenBudgetPolicy uint8

const (
	// TokenBudgetExact sends the portable max-token value unchanged.
	TokenBudgetExact TokenBudgetPolicy = iota + 1
	// TokenBudgetScaleForReasoning applies the configured multiplier only when
	// a nonempty reasoning effort other than "none" is active.
	TokenBudgetScaleForReasoning
)

// ReasoningEffortStyle selects the supported reasoning-effort spelling.
type ReasoningEffortStyle uint8

const (
	// ReasoningEffortOmitted emits no reasoning-effort field.
	ReasoningEffortOmitted ReasoningEffortStyle = iota + 1
	// ReasoningEffortTopLevel emits the reasoning_effort scalar.
	ReasoningEffortTopLevel
	// ReasoningEffortNestedObject emits a reasoning.effort object.
	ReasoningEffortNestedObject
)

// SamplingPolicy selects ordinary or reasoning-family sampling behavior.
type SamplingPolicy uint8

const (
	// SamplingOrdinary retains ordinary sampling controls.
	SamplingOrdinary SamplingPolicy = iota + 1
	// SamplingReasoningRestricted applies reasoning-family sampling restrictions.
	SamplingReasoningRestricted
)

// RequestProfile separates semantic model identity from the protected wire shape.
type RequestProfile struct {
	SemanticModel   string
	WireModel       string
	ModelField      ModelFieldMode
	TokenLimit      TokenLimitField
	TokenBudget     TokenBudgetPolicy
	ReasoningEffort ReasoningEffortStyle
	Sampling        SamplingPolicy
}

// Validate rejects incomplete and contradictory wire profiles.
func (p RequestProfile) Validate() error {
	if strings.TrimSpace(p.SemanticModel) == "" {
		return errors.New("OpenAI wire semantic model is empty")
	}
	switch p.ModelField {
	case ModelFieldRequired:
		if strings.TrimSpace(p.WireModel) == "" {
			return errors.New("OpenAI wire model is required by the profile")
		}
	case ModelFieldOmitted:
		if p.WireModel != "" {
			return errors.New("OpenAI wire model must be empty when the body field is omitted")
		}
	default:
		return errors.New("OpenAI wire model-field mode is invalid")
	}
	if p.TokenLimit != TokenLimitMaxTokens && p.TokenLimit != TokenLimitMaxCompletionTokens {
		return errors.New("OpenAI wire token-limit field is invalid")
	}
	if p.TokenBudget != TokenBudgetExact && p.TokenBudget != TokenBudgetScaleForReasoning {
		return errors.New("OpenAI wire token-budget policy is invalid")
	}
	if p.TokenBudget == TokenBudgetScaleForReasoning && p.TokenLimit != TokenLimitMaxCompletionTokens {
		return errors.New("reasoning-scaled token budget requires max_completion_tokens")
	}
	if p.ReasoningEffort < ReasoningEffortOmitted || p.ReasoningEffort > ReasoningEffortNestedObject {
		return errors.New("OpenAI wire reasoning-effort style is invalid")
	}
	if p.Sampling != SamplingOrdinary && p.Sampling != SamplingReasoningRestricted {
		return errors.New("OpenAI wire sampling policy is invalid")
	}
	if p.Sampling == SamplingReasoningRestricted && p.TokenLimit != TokenLimitMaxCompletionTokens {
		return errors.New("reasoning-restricted sampling requires max_completion_tokens")
	}
	return nil
}

func profileFingerprintIdentity(surfaceVersion string, profile RequestProfile) string {
	return fmt.Sprintf(
		"%s|%s|model=%d|tokens=%d|budget=%d|reasoning=%d|sampling=%d",
		surfaceVersion,
		profileVersion,
		profile.ModelField,
		profile.TokenLimit,
		profile.TokenBudget,
		profile.ReasoningEffort,
		profile.Sampling,
	)
}
