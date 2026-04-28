package orchestration

import "github.com/truvaagents/truva-g3/core"

// AIOptionsOverride represents explicit per-phase overrides for AIOptions.
// Nil fields mean "leave the phase default unchanged".
type AIOptionsOverride struct {
	Model           *string                `json:"model,omitempty"`
	Temperature     *float32               `json:"temperature,omitempty"`
	MaxTokens       *int                   `json:"max_tokens,omitempty"`
	SystemPrompt    *string                `json:"system_prompt,omitempty"`
	ReasoningEffort *string                `json:"reasoning_effort,omitempty"`
	ResponseFormat  *string                `json:"response_format,omitempty"`
	Extra           map[string]interface{} `json:"extra,omitempty"`
	Headers         map[string]string      `json:"headers,omitempty"`
}

func IntPtr(v int) *int { return &v }

func Float32Ptr(v float32) *float32 { return &v }

func StringPtr(v string) *string { return &v }

func roundLegacyFloat(v float64) float64 {
	const places = 1_000_000
	if v == 0 {
		return 0
	}
	if v > 0 {
		return float64(int(v*places+0.5)) / places
	}
	return float64(int(v*places-0.5)) / places
}

func mergeAIOptions(base *core.AIOptions, override *AIOptionsOverride) *core.AIOptions {
	if base == nil {
		base = &core.AIOptions{}
	}
	if override == nil {
		return cloneCoreAIOptions(base)
	}

	merged := cloneCoreAIOptions(base)

	if override.Model != nil {
		merged.Model = *override.Model
	}
	if override.Temperature != nil {
		merged.Temperature = *override.Temperature
	}
	if override.MaxTokens != nil {
		merged.MaxTokens = *override.MaxTokens
	}
	if override.SystemPrompt != nil {
		merged.SystemPrompt = *override.SystemPrompt
	}
	if override.ReasoningEffort != nil {
		merged.ReasoningEffort = *override.ReasoningEffort
	}
	if override.ResponseFormat != nil {
		merged.ResponseFormat = *override.ResponseFormat
	}
	if override.Extra != nil {
		merged.Extra = make(map[string]interface{}, len(override.Extra))
		for k, v := range override.Extra {
			merged.Extra[k] = v
		}
	}
	if override.Headers != nil {
		merged.Headers = make(map[string]string, len(override.Headers))
		for k, v := range override.Headers {
			merged.Headers[k] = v
		}
	}

	return merged
}

func cloneCoreAIOptions(src *core.AIOptions) *core.AIOptions {
	if src == nil {
		return nil
	}

	cloned := *src
	if src.Extra != nil {
		cloned.Extra = make(map[string]interface{}, len(src.Extra))
		for k, v := range src.Extra {
			cloned.Extra[k] = v
		}
	}
	if src.Headers != nil {
		cloned.Headers = make(map[string]string, len(src.Headers))
		for k, v := range src.Headers {
			cloned.Headers[k] = v
		}
	}

	return &cloned
}

func applyLegacyAIOptionFields(config *OrchestratorConfig) {
	if config == nil || !config.legacyAIOptionBridge {
		return
	}

	if config.PlanAIOptions == nil {
		config.PlanAIOptions = &AIOptionsOverride{}
	}
	if config.PlanMaxTokens > 0 && config.PlanAIOptions.MaxTokens == nil {
		config.PlanAIOptions.MaxTokens = IntPtr(config.PlanMaxTokens)
	}
	if config.PlanModel != "" && config.PlanAIOptions.Model == nil {
		config.PlanAIOptions.Model = StringPtr(config.PlanModel)
	}

	if config.SynthesisAIOptions == nil {
		config.SynthesisAIOptions = &AIOptionsOverride{}
	}
	if config.SynthesisMaxTokens > 0 && config.SynthesisAIOptions.MaxTokens == nil {
		config.SynthesisAIOptions.MaxTokens = IntPtr(config.SynthesisMaxTokens)
	}
	if config.SynthesisAIOptions.Temperature == nil {
		config.SynthesisAIOptions.Temperature = Float32Ptr(float32(config.SynthesisTemperature))
	}
	if config.SynthesisModel != "" && config.SynthesisAIOptions.Model == nil {
		config.SynthesisAIOptions.Model = StringPtr(config.SynthesisModel)
	}

	if config.MicroResolutionAIOptions == nil {
		config.MicroResolutionAIOptions = &AIOptionsOverride{}
	}
	if config.MicroResolutionMaxTokens > 0 && config.MicroResolutionAIOptions.MaxTokens == nil {
		config.MicroResolutionAIOptions.MaxTokens = IntPtr(config.MicroResolutionMaxTokens)
	}
	if config.MicroResolutionModel != "" && config.MicroResolutionAIOptions.Model == nil {
		config.MicroResolutionAIOptions.Model = StringPtr(config.MicroResolutionModel)
	}
}
