package azureopenai

import (
	"errors"
	"fmt"
	"strings"

	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/core"
)

type surface uint8

const (
	surfaceV1 surface = iota + 1
	surfaceClassic
)

const (
	surfaceVersionV1      = "azure-openai-v1-chat-v1"
	surfaceVersionClassic = "azure-openai-classic-chat-v1"
)

func parseSurface(alias string) (surface, error) {
	switch alias {
	case "azureopenai.v1":
		return surfaceV1, nil
	case "azureopenai.classic":
		return surfaceClassic, nil
	default:
		return 0, fmt.Errorf("unsupported Azure OpenAI provider alias %q", alias)
	}
}

func (selected surface) surfaceVersion() (string, error) {
	switch selected {
	case surfaceV1:
		return surfaceVersionV1, nil
	case surfaceClassic:
		return surfaceVersionClassic, nil
	default:
		return "", errors.New("azure OpenAI surface is invalid")
	}
}

type surfaceContract struct {
	supportsOpenAIReasoning bool
}

func (c *Client) surfaceContract(route resolvedRoute) (surfaceContract, error) {
	switch c.surface {
	case surfaceV1:
		return surfaceContract{supportsOpenAIReasoning: true}, nil
	case surfaceClassic:
		versions := route.url.Query()["api-version"]
		if len(versions) != 1 {
			return surfaceContract{}, errors.New("azure OpenAI classic api-version is invalid")
		}
		// The Phase 9 baseline pins the ordinary classic surface to the GA
		// 2024-10-21 schema. No classic version has a verified reasoning row yet.
		return surfaceContract{}, nil
	default:
		return surfaceContract{}, errors.New("azure OpenAI surface is invalid")
	}
}

func (c *Client) requestProfile(
	semantics *requestSemantics,
	route resolvedRoute,
) (openaiwire.RequestProfile, error) {
	if strings.TrimSpace(route.deployment) == "" {
		return openaiwire.RequestProfile{}, errors.New("azure OpenAI deployment is empty")
	}
	contract, err := c.surfaceContract(route)
	if err != nil {
		return openaiwire.RequestProfile{}, err
	}
	profile := openaiwire.RequestProfile{
		SemanticModel:   semantics.SemanticModel,
		TokenLimit:      openaiwire.TokenLimitMaxTokens,
		TokenBudget:     openaiwire.TokenBudgetExact,
		ReasoningEffort: openaiwire.ReasoningEffortOmitted,
		Sampling:        openaiwire.SamplingOrdinary,
	}
	if semantics.ReasoningModel {
		if !contract.supportsOpenAIReasoning {
			return openaiwire.RequestProfile{}, fmt.Errorf(
				"%w: Azure OpenAI surface %q does not have a verified reasoning contract",
				core.ErrAIRequestFeatureUnsupported,
				semantics.ProviderAlias,
			)
		}
		profile.TokenLimit = openaiwire.TokenLimitMaxCompletionTokens
		profile.TokenBudget = openaiwire.TokenBudgetScaleForReasoning
		profile.Sampling = openaiwire.SamplingReasoningRestricted
	}
	if contract.supportsOpenAIReasoning && semantics.Capabilities.ReasoningStyle == "openai" {
		profile.ReasoningEffort = openaiwire.ReasoningEffortTopLevel
	}
	switch c.surface {
	case surfaceV1:
		profile.ModelField = openaiwire.ModelFieldRequired
		profile.WireModel = route.deployment
	case surfaceClassic:
		profile.ModelField = openaiwire.ModelFieldOmitted
	default:
		return openaiwire.RequestProfile{}, errors.New("azure OpenAI surface is invalid")
	}
	return profile, profile.Validate()
}
