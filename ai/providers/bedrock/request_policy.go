//go:build bedrock
// +build bedrock

package bedrock

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

const bedrockSamplingRule = "bedrock-current-claude-sampling"

type bedrockSamplingPolicy uint8

const (
	bedrockSamplingUnrestricted bedrockSamplingPolicy = iota
	bedrockSamplingOmitAll
	bedrockSamplingFable5
)

type bedrockSamplingFamily struct {
	modelFamily string
	policy      bedrockSamplingPolicy
}

// bedrockSamplingFamilies is the single source of truth for model-family
// sampling behavior on the Converse surface. Mythos is intentionally absent:
// its current Bedrock model cards expose the Messages API rather than Converse.
var bedrockSamplingFamilies = []bedrockSamplingFamily{
	{modelFamily: "anthropic.claude-opus-4-7", policy: bedrockSamplingOmitAll},
	{modelFamily: "anthropic.claude-opus-4-8", policy: bedrockSamplingOmitAll},
	{modelFamily: "anthropic.claude-sonnet-5", policy: bedrockSamplingOmitAll},
	{modelFamily: "anthropic.claude-fable-5", policy: bedrockSamplingFable5},
}

type bedrockRequestPolicy struct {
	engines map[bedrockSamplingPolicy]*requestpolicy.Engine
}

func newRequestPolicyEngine() *bedrockRequestPolicy {
	engine, err := newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in Bedrock request policy: %v", err))
	}
	return engine
}

func newRequestPolicyEngineWithIntegration(
	appRules []core.AIProviderPatch,
	middleware []requestpolicy.RequestMiddleware,
	mode requestpolicy.CompatibilityMode,
) (*bedrockRequestPolicy, error) {
	rules := map[bedrockSamplingPolicy][]core.AIProviderPatch{
		bedrockSamplingUnrestricted: nil,
		bedrockSamplingOmitAll: {
			{
				Name:    bedrockSamplingRule,
				Version: "2",
				Selector: core.AIProviderSelector{
					Provider: "bedrock",
					Surface:  "converse",
				},
				Remove: []string{
					"/inference_config/temperature",
					"/inference_config/top_p",
					"/additional_model_request_fields/top_k",
				},
			},
		},
		bedrockSamplingFable5: {
			{
				Name:    bedrockSamplingRule,
				Version: "2",
				Selector: core.AIProviderSelector{
					Provider: "bedrock",
					Surface:  "converse",
				},
				Remove: []string{
					"/additional_model_request_fields/top_k",
				},
			},
		},
	}
	policy := &bedrockRequestPolicy{
		engines: make(map[bedrockSamplingPolicy]*requestpolicy.Engine, len(rules)),
	}
	for behavior, builtIns := range rules {
		engine, err := requestpolicy.NewEngine(requestpolicy.Config{
			BuiltIns:   builtIns,
			AppRules:   appRules,
			Middleware: middleware,
			Mode:       mode,
		})
		if err != nil {
			return nil, err
		}
		policy.engines[behavior] = engine
	}
	return policy, nil
}

func (p *bedrockRequestPolicy) Apply(
	ctx context.Context,
	draft *Draft,
	perRequest []core.AIProviderPatch,
) (*core.AIRequestReport, error) {
	if p == nil {
		return nil, errors.New("bedrock request policy is nil")
	}
	if draft == nil {
		return nil, errors.New("bedrock request draft is nil")
	}
	behavior := bedrockSamplingPolicyForModel(draft.semanticModel)
	engine := p.engines[behavior]
	if engine == nil {
		return nil, fmt.Errorf("bedrock request policy has no engine for sampling behavior %d", behavior)
	}
	return engine.Apply(ctx, draft, perRequest)
}

func bedrockSamplingPolicyForModel(model string) bedrockSamplingPolicy {
	for _, family := range bedrockSamplingFamilies {
		if bedrockModelInFamily(model, family.modelFamily) {
			return family.policy
		}
	}
	return bedrockSamplingUnrestricted
}

func bedrockModelInFamily(model, family string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	for offset := 0; ; {
		index := strings.Index(normalized[offset:], family)
		if index < 0 {
			return false
		}
		index += offset
		beforeOK := index == 0 || !isModelIdentifierCharacter(normalized[index-1])
		after := index + len(family)
		afterOK := after == len(normalized) || !isModelIdentifierCharacter(normalized[after])
		if beforeOK && afterOK {
			return true
		}
		offset = index + 1
	}
}

func isModelIdentifierCharacter(character byte) bool {
	return character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9' ||
		character == '_'
}
