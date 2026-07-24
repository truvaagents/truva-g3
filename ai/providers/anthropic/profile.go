package anthropic

import (
	"errors"
	"strings"
)

type modelFieldMode uint8
type versionPlacement uint8

const (
	modelInBody modelFieldMode = iota + 1
	modelInRoute
)

const (
	versionInHeader versionPlacement = iota + 1
	versionInBody
)

const (
	directProfileIdentity = "anthropic-messages-v1"
	vertexProfileIdentity = "anthropic-vertex-predict-v1"
	vertexAPIVersion      = "vertex-2023-10-16"
)

type requestProfile struct {
	fingerprintIdentity string
	semanticModel       string
	wireModel           string
	modelField          modelFieldMode
	versionPlacement    versionPlacement
	version             string
}

func (profile requestProfile) validate() error {
	if strings.TrimSpace(profile.fingerprintIdentity) == "" {
		return errors.New("anthropic profile identity is empty")
	}
	if strings.TrimSpace(profile.semanticModel) == "" {
		return errors.New("anthropic semantic model is empty")
	}
	if strings.TrimSpace(profile.wireModel) == "" {
		return errors.New("anthropic wire model is empty")
	}
	if profile.modelField != modelInBody && profile.modelField != modelInRoute {
		return errors.New("anthropic model-field mode is invalid")
	}
	if profile.versionPlacement != versionInHeader && profile.versionPlacement != versionInBody {
		return errors.New("anthropic version placement is invalid")
	}
	if strings.TrimSpace(profile.version) == "" {
		return errors.New("anthropic profile version is empty")
	}
	return nil
}

func (c *Client) requestProfile(
	semantics *requestSemantics,
	route resolvedRoute,
) (requestProfile, error) {
	if semantics.ProviderAlias != "anthropic.vertex" {
		profile := requestProfile{
			fingerprintIdentity: directProfileIdentity,
			semanticModel:       semantics.SemanticModel,
			wireModel:           semantics.SemanticModel,
			modelField:          modelInBody,
			versionPlacement:    versionInHeader,
			version:             APIVersion,
		}
		return profile, profile.validate()
	}
	if strings.TrimSpace(route.deployment) == "" {
		return requestProfile{}, errors.New("vertex Anthropic publisher model is empty")
	}
	profile := requestProfile{
		fingerprintIdentity: vertexProfileIdentity,
		semanticModel:       semantics.SemanticModel,
		wireModel:           route.deployment,
		modelField:          modelInRoute,
		versionPlacement:    versionInBody,
		version:             vertexAPIVersion,
	}
	return profile, profile.validate()
}
