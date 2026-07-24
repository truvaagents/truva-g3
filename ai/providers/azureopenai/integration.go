package azureopenai

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

type resolvedRoute struct {
	url             *url.URL
	identity        string
	deployment      string
	credentialScope string
}

type integrationInvocationError struct {
	stage string
	cause error
}

func (e *integrationInvocationError) Error() string {
	return fmt.Sprintf("Azure OpenAI %s failed", e.stage)
}

func (e *integrationInvocationError) Unwrap() error { return e.cause }

func (c *Client) withRequestTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.requestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.requestTimeout)
}

func (c *Client) resolveEndpoint(ctx context.Context, request ai.EndpointRequest) (resolvedRoute, error) {
	resolved, err := c.endpointResolver.ResolveEndpoint(ctx, request)
	if err != nil {
		return resolvedRoute{}, &integrationInvocationError{stage: "endpoint resolution", cause: err}
	}
	snapshot, err := snapshotResolvedEndpoint(resolved)
	if err != nil {
		return resolvedRoute{}, err
	}
	route := resolvedRoute{
		url:             snapshot.URL,
		identity:        snapshot.RouteIdentity,
		deployment:      snapshot.Deployment,
		credentialScope: snapshot.CredentialScope,
	}
	if err := validateResolvedRoute(c.surface, route); err != nil {
		return resolvedRoute{}, err
	}
	return route, nil
}

func snapshotResolvedEndpoint(resolved ai.ResolvedEndpoint) (ai.ResolvedEndpoint, error) {
	if resolved.URL == nil {
		return ai.ResolvedEndpoint{}, errors.New("azure OpenAI resolved endpoint URL is nil")
	}
	clonedURL := *resolved.URL
	query := clonedURL.Query()
	for key, values := range resolved.Query {
		query[key] = append([]string(nil), values...)
	}
	clonedURL.RawQuery = query.Encode()
	return ai.ResolvedEndpoint{
		URL:             &clonedURL,
		Deployment:      resolved.Deployment,
		RouteIdentity:   resolved.RouteIdentity,
		CredentialScope: resolved.CredentialScope,
		Query:           cloneURLValues(resolved.Query),
	}, nil
}

func validateResolvedRoute(selected surface, route resolvedRoute) error {
	endpoint := route.url
	if endpoint == nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" {
		return errors.New("azure OpenAI endpoint must use HTTPS with a nonempty host")
	}
	if endpoint.User != nil || endpoint.Port() != "" || endpoint.Fragment != "" {
		return errors.New("azure OpenAI endpoint must not contain user information, a port, or a fragment")
	}
	if err := validateRouteIdentity(route.identity); err != nil {
		return err
	}
	if strings.TrimSpace(route.deployment) == "" {
		return errors.New("azure OpenAI deployment is empty")
	}

	switch selected {
	case surfaceV1:
		if endpoint.EscapedPath() != "/openai/v1/chat/completions" {
			return errors.New("azure OpenAI v1 endpoint path is invalid")
		}
		versions := endpoint.Query()["api-version"]
		if len(versions) > 1 || len(versions) == 1 && strings.TrimSpace(versions[0]) == "" {
			return errors.New("azure OpenAI v1 api-version must be singular and nonempty when supplied")
		}
	case surfaceClassic:
		want := "/openai/deployments/" + url.PathEscape(route.deployment) + "/chat/completions"
		if endpoint.EscapedPath() != want {
			return errors.New("azure OpenAI classic endpoint path does not match deployment")
		}
		versions := endpoint.Query()["api-version"]
		if len(versions) != 1 || strings.TrimSpace(versions[0]) == "" {
			return errors.New("azure OpenAI classic endpoint requires exactly one api-version")
		}
	default:
		return errors.New("azure OpenAI surface is invalid")
	}
	return nil
}

func validateRouteIdentity(identity string) error {
	if strings.TrimSpace(identity) == "" {
		return errors.New("azure OpenAI route identity is empty")
	}
	if len(identity) > 256 {
		return errors.New("azure OpenAI route identity exceeds 256 bytes")
	}
	for _, character := range identity {
		if character == 0x7f || character < 0x20 {
			return errors.New("azure OpenAI route identity contains control characters")
		}
	}
	return nil
}

func cloneURLValues(source url.Values) url.Values {
	if source == nil {
		return nil
	}
	clone := make(url.Values, len(source))
	for key, values := range source {
		clone[key] = append([]string(nil), values...)
	}
	return clone
}

func (c *Client) credentialRequest(prepared *preparedRequest, route resolvedRoute) ai.CredentialRequest {
	request := ai.CredentialRequest{
		Provider:        providerName,
		ProviderAlias:   c.providerAlias,
		Surface:         "chat-completions",
		ResolvedModel:   prepared.SemanticModel,
		RouteIdentity:   route.identity,
		Deployment:      route.deployment,
		CredentialScope: route.credentialScope,
	}
	if prepared.Report != nil {
		request.Operation = prepared.Report.Operation
	}
	return request
}

func (c *Client) prepareCredential(
	ctx context.Context,
	attempt *http.Request,
	identity ai.CredentialRequest,
) error {
	if c.credentialSource == nil {
		if strings.TrimSpace(c.staticAPIKey) == "" || containsInvalidHTTPHeaderValue(c.staticAPIKey) {
			return errors.New("azure OpenAI API key is invalid")
		}
		if attempt.Header.Values("api-key") != nil {
			return errors.New("azure OpenAI api-key conflicts with prepared headers")
		}
		attempt.Header.Set("api-key", c.staticAPIKey)
		return nil
	}
	credential, err := c.credentialSource.Credential(ctx, identity)
	if err != nil {
		return &integrationInvocationError{stage: "credential acquisition", cause: err}
	}
	if err := validateAzureCredential(credential); err != nil {
		return err
	}
	if attempt.Header.Values(credential.Name) != nil {
		return fmt.Errorf("azure OpenAI credential header %q conflicts with prepared headers", credential.Name)
	}
	attempt.Header.Set(credential.Name, credential.Value)
	return nil
}

func validateAzureCredential(credential ai.HeaderCredential) error {
	if strings.TrimSpace(credential.Value) == "" || containsInvalidHTTPHeaderValue(credential.Value) {
		return errors.New("azure OpenAI credential value is invalid")
	}
	switch {
	case strings.EqualFold(credential.Name, "api-key"):
		return nil
	case strings.EqualFold(credential.Name, "Authorization"):
		parts := strings.SplitN(credential.Value, " ", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && strings.TrimSpace(parts[1]) != "" {
			return nil
		}
	}
	return errors.New("azure OpenAI credential must be api-key or Authorization: Bearer")
}

func containsInvalidHTTPHeaderValue(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == 0x7f || character < 0x20 && character != '\t' {
			return true
		}
	}
	return false
}

func (c *Client) observeCredentialRejection(
	ctx context.Context,
	request ai.CredentialRequest,
	statusCode int,
) {
	if c.credentialSource == nil || statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden {
		return
	}
	observer, ok := c.credentialSource.(ai.CredentialRejectionObserver)
	if !ok {
		return
	}
	if err := observer.CredentialRejected(ctx, request, statusCode); err != nil && c.Logger != nil {
		errorType, safeError := providers.SanitizedObservationError(err, "callback")
		fields := map[string]interface{}{
			"operation": "ai_credential_rejection_observer", "provider": providerName,
			"provider_alias": c.providerAlias, "status_code": statusCode,
			"error": safeError.Error(), "error_type": errorType,
		}
		providers.AddObservationRequestID(ctx, fields)
		c.Logger.WarnWithContext(ctx, "Azure OpenAI credential rejection observer failed", fields)
	}
}

func providerHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return &http.Client{Transport: http.DefaultTransport}
	}
	clone := *client
	if clone.Transport == nil {
		clone.Transport = http.DefaultTransport
	}
	return &clone
}

func bindRoute(prepared *preparedRequest, route resolvedRoute) {
	if prepared == nil || prepared.Report == nil {
		return
	}
	prepared.Report.Adjustments = append(prepared.Report.Adjustments, core.AIRequestAdjustment{
		Source: "endpoint-resolver", Rule: route.identity, Path: "/route",
		Action: "resolve", Reason: "trusted endpoint resolution",
	})
	if prepared.Report.Stable && prepared.Report.Fingerprint != "" {
		sum := sha256.Sum256([]byte("policy=" + prepared.Report.Fingerprint + "\nroute=" + route.identity))
		prepared.Report.Fingerprint = fmt.Sprintf("%x", sum[:])
	}
}
