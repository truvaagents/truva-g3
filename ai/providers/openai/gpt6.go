package openai

import (
	"fmt"

	"github.com/truvaagents/truva-g3/ai/internal/openaimodel"
	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

// Model-specific API restrictions belong to the native provider, not every
// endpoint that happens to speak the OpenAI wire protocol. Validate after
// application policy so developers can repair inherited native parameters.
type gpt6Draft struct {
	*openaiwire.Draft
}

func (d *gpt6Draft) PolicyFingerprintIdentity() string {
	return d.Draft.PolicyFingerprintIdentity() + "|gpt6-chat-v1"
}

func (d *gpt6Draft) Validate() error {
	if err := d.Draft.Validate(); err != nil {
		return err
	}
	for _, name := range []string{"reasoning_effort", "temperature", "top_p", "top_logprobs", "logprobs", "tools", "tool_choice", "functions", "function_call"} {
		if containsNonCanonicalKey(d.Body(), name) {
			return gpt6ParameterError(name, "use the canonical lowercase field name")
		}
	}
	effort := ""
	if value, present := d.Get("/reasoning_effort"); present {
		var ok bool
		effort, ok = value.(string)
		if !ok || !validGPT6Effort(d.Info().ResolvedModel, effort) {
			return gpt6ParameterError("reasoning_effort", "use low, medium, high, xhigh or max; none is supported only by Sol and Luna")
		}
	}
	if effort != "none" {
		for _, name := range []string{"temperature", "top_p", "top_logprobs", "logprobs"} {
			if _, present := d.Get("/" + name); present {
				return gpt6ParameterError(name, "remove sampling/logprob controls when reasoning is active")
			}
		}
		for _, name := range []string{"tools", "tool_choice", "functions", "function_call"} {
			if _, present := d.Get("/" + name); present {
				return gpt6ParameterError(name, "reasoning with native tools requires a Responses API adapter; Sol and Luna allow Chat Completions tools with reasoning_effort=none")
			}
		}
	}
	return nil
}

func validGPT6Effort(model, effort string) bool {
	switch effort {
	case "low", "medium", "high", "xhigh", "max":
		return true
	case "none":
		return openaimodel.SupportsGPT6NoReasoning(model)
	default:
		return false
	}
}

func gpt6ParameterError(name, guidance string) error {
	return &requestpolicy.PolicyError{
		Stage: "gpt6-chat-contract",
		Path:  "/" + name,
		Err: fmt.Errorf("%s: %w", guidance, &core.AIRequestFeatureError{
			ClientType: "*openai.Client",
			Feature:    "gpt6.chat_completions." + name,
		}),
	}
}
