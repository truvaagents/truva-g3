//go:build bedrock
// +build bedrock

package bedrock

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/truvaagents/truva-g3/ai"
)

const (
	defaultRouteIdentity  = "bedrock.direct"
	maxRouteIdentityBytes = 256
	maxWireModelBytes     = 2048
)

type requestProfile struct {
	semanticModel string
	wireModel     string
	routeIdentity string
}

type routeResolutionError struct{ cause error }

func (e *routeResolutionError) Error() string {
	if e == nil || e.cause == nil {
		return "Bedrock endpoint resolution failed"
	}
	return fmt.Sprintf("Bedrock endpoint resolution failed: %v", e.cause)
}
func (e *routeResolutionError) Unwrap() error { return e.cause }

func (c *Client) resolveProfile(
	ctx context.Context,
	request ai.EndpointRequest,
) (requestProfile, error) {
	if strings.TrimSpace(request.ResolvedModel) == "" {
		return requestProfile{}, errors.New("bedrock semantic model is empty")
	}
	if c.endpointResolver == nil {
		if err := validateWireModel(request.ResolvedModel); err != nil {
			return requestProfile{}, err
		}
		return requestProfile{
			semanticModel: request.ResolvedModel,
			wireModel:     request.ResolvedModel,
			routeIdentity: defaultRouteIdentity,
		}, nil
	}

	resolved, err := c.endpointResolver.ResolveEndpoint(ctx, request)
	if err != nil {
		return requestProfile{}, newRouteResolutionError(err)
	}
	if resolved.URL != nil {
		return requestProfile{}, newRouteResolutionError(errors.New("bedrock SDK route must not contain a URL"))
	}
	if len(resolved.Query) != 0 {
		return requestProfile{}, newRouteResolutionError(errors.New("bedrock SDK route must not contain query parameters"))
	}
	if resolved.CredentialScope != "" {
		return requestProfile{}, newRouteResolutionError(errors.New("bedrock SDK route must not contain a credential scope"))
	}
	if err := validateWireModel(resolved.Deployment); err != nil {
		return requestProfile{}, newRouteResolutionError(err)
	}
	if err := validateBedrockRouteIdentity(resolved.RouteIdentity); err != nil {
		return requestProfile{}, newRouteResolutionError(err)
	}
	return requestProfile{
		semanticModel: request.ResolvedModel,
		wireModel:     resolved.Deployment,
		routeIdentity: resolved.RouteIdentity,
	}, nil
}

func newRouteResolutionError(err error) error {
	return &routeResolutionError{cause: err}
}

func validateWireModel(model string) error {
	return validateModelID(model, "bedrock resolved endpoint deployment")
}

func validateModelID(model string, subject string) error {
	if strings.TrimSpace(model) == "" {
		return fmt.Errorf("%s is empty", subject)
	}
	if strings.TrimSpace(model) != model {
		return fmt.Errorf("%s has surrounding whitespace", subject)
	}
	if len(model) > maxWireModelBytes {
		return fmt.Errorf("%s exceeds %d bytes", subject, maxWireModelBytes)
	}
	for _, character := range model {
		if character == 0x7f || character < 0x20 {
			return fmt.Errorf("%s contains control characters", subject)
		}
	}
	return nil
}

func validateBedrockRouteIdentity(identity string) error {
	if strings.TrimSpace(identity) == "" {
		return errors.New("bedrock resolved endpoint route identity is empty")
	}
	if strings.TrimSpace(identity) != identity {
		return errors.New("bedrock resolved endpoint route identity has surrounding whitespace")
	}
	if len(identity) > maxRouteIdentityBytes {
		return fmt.Errorf("bedrock resolved endpoint route identity exceeds %d bytes", maxRouteIdentityBytes)
	}
	for _, character := range identity {
		if character == 0x7f || character < 0x20 {
			return errors.New("bedrock resolved endpoint route identity contains control characters")
		}
	}
	return nil
}
