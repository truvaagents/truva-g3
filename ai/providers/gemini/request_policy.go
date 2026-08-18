package gemini

import (
	"fmt"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

const (
	samplingAdjustmentRule  = "gemini-latest-model-sampling-v1"
	candidateAdjustmentRule = "gemini-3-candidate-count-v1"
)

func newRequestPolicyEngine() *requestpolicy.Engine {
	engine, err := newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in Gemini request policy: %v", err))
	}
	return engine
}

func newRequestPolicyEngineWithIntegration(
	appRules []core.AIProviderPatch,
	middleware []requestpolicy.RequestMiddleware,
	mode requestpolicy.CompatibilityMode,
) (*requestpolicy.Engine, error) {
	rules := make([]core.AIProviderPatch, 0)
	for _, capabilities := range capabilitySnapshot {
		var removeSampling []string
		if capabilities.ForbidTemperature {
			removeSampling = append(removeSampling, "/generationConfig/temperature")
		}
		if capabilities.ForbidTopP {
			removeSampling = append(removeSampling, "/generationConfig/topP")
		}
		if capabilities.ForbidTopK {
			removeSampling = append(removeSampling, "/generationConfig/topK")
		}
		if len(removeSampling) > 0 {
			rules = append(rules, core.AIProviderPatch{
				Name:    samplingAdjustmentRule,
				Version: "1",
				Selector: core.AIProviderSelector{
					Provider: "gemini",
					Surface:  "generate-content",
					Model:    capabilities.ModelID,
				},
				Remove: removeSampling,
			})
		}
		if capabilities.ForbidCandidateCount {
			rules = append(rules, core.AIProviderPatch{
				Name:    candidateAdjustmentRule,
				Version: "1",
				Selector: core.AIProviderSelector{
					Provider: "gemini",
					Surface:  "generate-content",
					Model:    capabilities.ModelID,
				},
				Remove: []string{"/generationConfig/candidateCount"},
			})
		}
	}
	return requestpolicy.NewEngine(requestpolicy.Config{
		BuiltIns:   rules,
		AppRules:   appRules,
		Middleware: middleware,
		Mode:       mode,
	})
}
