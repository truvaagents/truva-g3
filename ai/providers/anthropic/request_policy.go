package anthropic

import (
	"fmt"
	"strings"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type samplingPolicy uint8

const (
	samplingUnknown samplingPolicy = iota
	samplingAllowed
	samplingOmitted
)

var omitSamplingPrefixes = []string{
	"claude-opus-4-7",
	"claude-opus-4-8",
	"claude-sonnet-5",
	"claude-fable-5",
	"claude-mythos-5",
	"claude-mythos-preview",
}

func newRequestPolicyEngine() *requestpolicy.Engine {
	engine, err := newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in Anthropic request policy: %v", err))
	}
	return engine
}

func newRequestPolicyEngineWithIntegration(
	appRules []core.AIProviderPatch,
	middleware []requestpolicy.RequestMiddleware,
	mode requestpolicy.CompatibilityMode,
) (*requestpolicy.Engine, error) {
	rules := make([]core.AIProviderPatch, 0, len(omitSamplingPrefixes)*2)
	for _, family := range omitSamplingPrefixes {
		for _, modelSelector := range []string{family, family + "-*"} {
			rules = append(rules, core.AIProviderPatch{
				Name:    samplingAdjustmentRule,
				Version: "1",
				Selector: core.AIProviderSelector{
					Provider: "anthropic",
					Surface:  "messages",
					Model:    modelSelector,
				},
				Remove: []string{"/temperature", "/top_p", "/top_k"},
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

func samplingPolicyForModel(model string) samplingPolicy {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range omitSamplingPrefixes {
		if modelInFamily(normalized, prefix) {
			return samplingOmitted
		}
	}
	if modelInFamily(normalized, "claude-sonnet-4-6") ||
		modelInFamily(normalized, "claude-haiku-4-5") {
		return samplingAllowed
	}
	return samplingUnknown
}

func modelInFamily(normalizedModel, normalizedFamily string) bool {
	return normalizedModel == normalizedFamily ||
		strings.HasPrefix(normalizedModel, normalizedFamily+"-")
}

func (policy samplingPolicy) String() string {
	switch policy {
	case samplingAllowed:
		return "allowed"
	case samplingOmitted:
		return "omitted"
	default:
		return "unknown"
	}
}
