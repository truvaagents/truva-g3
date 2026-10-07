package openai

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

// openRouterSolDraft validates the reviewed routed contract after application
// policy. Native tool restrictions are deliberately not imported: OpenRouter
// owns its upstream API translation. The shared codec still validates first.
type openRouterSolDraft struct{ *openaiwire.Draft }

func (d *openRouterSolDraft) PolicyFingerprintIdentity() string {
	return d.Draft.PolicyFingerprintIdentity() + "|openrouter-sol-6.1-v3"
}

func (d *openRouterSolDraft) Validate() error {
	if err := d.Draft.Validate(); err != nil {
		return err
	}
	for _, field := range []string{"temperature", "top_p", "top_k", "logprobs", "top_logprobs", "reasoning_effort"} {
		if containsNonCanonicalKey(d.Body(), field) {
			return openRouterSolParameterError(field, "use canonical parameter names")
		}
		if _, present := d.Get("/" + field); present {
			return openRouterSolParameterError(field, "omit sampling/logprob controls and use reasoning.effort for effort")
		}
	}
	if containsNonCanonicalKey(d.Body(), "reasoning") {
		return openRouterSolParameterError("reasoning", "use canonical parameter names")
	}
	if value, present := d.Get("/reasoning"); present {
		// JSON-native extras retain typed/named maps during defensive cloning.
		// Inspect their shape without replacing application-owned values.
		object := reflect.ValueOf(value)
		if !object.IsValid() || object.Kind() != reflect.Map || object.Type().Key().Kind() != reflect.String || object.IsNil() {
			return openRouterSolParameterError("reasoning", "use an object for reasoning controls")
		}
		for _, field := range []string{"effort", "enabled", "exclude", "max_tokens"} {
			if containsNonCanonicalMapKey(value, field) {
				return openRouterSolParameterError("reasoning/"+field, "use canonical parameter names")
			}
		}
	}
	if value, present := d.Get("/reasoning/effort"); present {
		effort := reflect.ValueOf(value)
		if !effort.IsValid() || effort.Kind() != reflect.String || !validGPT6Effort("gpt-6.1-sol", effort.String()) {
			return openRouterSolParameterError("reasoning/effort", "use low, medium, high, xhigh or max, or omit effort to use the model default")
		}
		if _, present := d.Get("/reasoning/max_tokens"); present {
			return openRouterSolParameterError("reasoning/max_tokens", "select either effort or max_tokens, not both")
		}
	}
	for _, field := range []string{"enabled", "exclude"} {
		if value, present := d.Get("/reasoning/" + field); present {
			flag := reflect.ValueOf(value)
			if !flag.IsValid() || flag.Kind() != reflect.Bool {
				return openRouterSolParameterError("reasoning/"+field, "use a boolean")
			}
			if field == "enabled" && !flag.Bool() {
				return openRouterSolParameterError("reasoning/enabled", "reasoning is required; use true or omit enabled")
			}
		}
	}
	if value, present := d.Get("/reasoning/max_tokens"); present && !positiveReasoningBudget(value) {
		return openRouterSolParameterError("reasoning/max_tokens", "use a positive integer reasoning budget")
	}
	return nil
}

// Validate the JSON number shape without converting caller-owned values or
// imposing an application token ceiling. OpenRouter owns budget-to-effort mapping.
func positiveReasoningBudget(value interface{}) bool {
	if number, ok := value.(json.Number); ok {
		parsed, err := number.Float64()
		return err == nil && positiveWholeNumber(parsed)
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() {
		return false
	}
	switch reflected.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return reflected.Int() > 0
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return reflected.Uint() > 0
	case reflect.Float32, reflect.Float64:
		return positiveWholeNumber(reflected.Float())
	default:
		return false
	}
}

func positiveWholeNumber(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && math.Trunc(value) == value
}

func openRouterSolParameterError(field, guidance string) error {
	return &requestpolicy.PolicyError{
		Stage: "openrouter-sol-contract", Path: "/" + field,
		Err: fmt.Errorf("%s: %w", guidance, &core.AIRequestFeatureError{
			ClientType: "*openai.Client", Feature: "openrouter.gpt-6.1-sol." + field,
		}),
	}
}
