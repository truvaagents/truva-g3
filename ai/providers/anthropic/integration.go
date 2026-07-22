package anthropic

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

const defaultRouteIdentity = "anthropic.default"

type resolvedRoute struct {
	url             *url.URL
	identity        string
	deployment      string
	credentialScope string
	custom          bool
}

type integrationInvocationError struct {
	stage string
	cause error
}

func (e *integrationInvocationError) Error() string {
	return fmt.Sprintf("anthropic %s failed", e.stage)
}

func (e *integrationInvocationError) Unwrap() error { return e.cause }

func (c *Client) withRequestTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.requestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.requestTimeout)
}

func (c *Client) resolveEndpoint(ctx context.Context, prepared *preparedRequest) (resolvedRoute, error) {
	if c.endpointResolver == nil {
		endpoint, err := parseAnthropicEndpoint(c.baseURL + "/messages")
		if err != nil {
			return resolvedRoute{}, err
		}
		return resolvedRoute{url: endpoint, identity: defaultRouteIdentity}, nil
	}

	request := ai.EndpointRequest{
		Provider:      "anthropic",
		ProviderAlias: c.providerAlias,
		Surface:       "messages",
		ResolvedModel: prepared.Model,
	}
	if prepared.Report != nil {
		request.Operation = prepared.Report.Operation
		request.Purpose = prepared.Report.Purpose
	}
	resolved, err := c.endpointResolver.ResolveEndpoint(ctx, request)
	if err != nil {
		return resolvedRoute{}, &integrationInvocationError{stage: "endpoint resolution", cause: err}
	}
	endpoint, err := snapshotResolvedEndpoint(resolved)
	if err != nil {
		return resolvedRoute{}, err
	}
	return resolvedRoute{
		url:             endpoint.URL,
		identity:        endpoint.RouteIdentity,
		deployment:      endpoint.Deployment,
		credentialScope: endpoint.CredentialScope,
		custom:          true,
	}, nil
}

func snapshotResolvedEndpoint(resolved ai.ResolvedEndpoint) (ai.ResolvedEndpoint, error) {
	if resolved.URL == nil {
		return ai.ResolvedEndpoint{}, errors.New("anthropic resolved endpoint URL is nil")
	}
	if err := validateRouteIdentity(resolved.RouteIdentity); err != nil {
		return ai.ResolvedEndpoint{}, err
	}
	clonedURL := *resolved.URL
	if _, err := parseAnthropicEndpoint(clonedURL.String()); err != nil {
		return ai.ResolvedEndpoint{}, err
	}

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

func parseAnthropicEndpoint(rawURL string) (*url.URL, error) {
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("anthropic endpoint URL is invalid")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("unsupported Anthropic endpoint scheme %q", endpoint.Scheme)
	}
	if endpoint.Host == "" {
		return nil, errors.New("anthropic endpoint host is empty")
	}
	if endpoint.User != nil {
		return nil, errors.New("anthropic endpoint must not contain user information")
	}
	if endpoint.Fragment != "" {
		return nil, errors.New("anthropic endpoint must not contain a fragment")
	}
	if endpoint.Path == "" || endpoint.Path[0] != '/' {
		return nil, errors.New("anthropic endpoint path is invalid")
	}
	clone := *endpoint
	return &clone, nil
}

func validateAnthropicBaseURL(rawURL string) error {
	endpoint, err := parseAnthropicEndpoint(strings.TrimRight(rawURL, "/") + "/messages")
	if err != nil {
		return err
	}
	if endpoint.RawQuery != "" {
		return errors.New("anthropic base URL must not contain query parameters")
	}
	return nil
}

func validateRouteIdentity(identity string) error {
	if strings.TrimSpace(identity) == "" {
		return errors.New("anthropic resolved endpoint route identity is empty")
	}
	if len(identity) > 256 {
		return errors.New("anthropic resolved endpoint route identity exceeds 256 bytes")
	}
	for _, character := range identity {
		if character == 0x7f || character < 0x20 {
			return errors.New("anthropic resolved endpoint route identity contains control characters")
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
		Provider:        "anthropic",
		ProviderAlias:   c.providerAlias,
		Surface:         "messages",
		ResolvedModel:   prepared.Model,
		RouteIdentity:   route.identity,
		Deployment:      route.deployment,
		CredentialScope: route.credentialScope,
	}
	if prepared.Report != nil {
		request.Operation = prepared.Report.Operation
	}
	return request
}

func (c *Client) executeWithCredential(
	ctx context.Context,
	request *http.Request,
	credentialRequest ai.CredentialRequest,
) (*http.Response, error) {
	if c.credentialSource == nil {
		return c.ExecuteWithRetry(ctx, request)
	}
	return c.ExecuteWithRetryPrepared(ctx, request, func(attemptCtx context.Context, attempt *http.Request) error {
		return c.prepareCredential(attemptCtx, attempt, credentialRequest)
	})
}

func (c *Client) prepareCredential(
	ctx context.Context,
	request *http.Request,
	credentialRequest ai.CredentialRequest,
) error {
	if c.credentialSource == nil {
		return nil
	}
	credential, err := c.credentialSource.Credential(ctx, credentialRequest)
	if err != nil {
		return &integrationInvocationError{stage: "credential acquisition", cause: err}
	}
	if err := validateHeaderCredential(credential); err != nil {
		return err
	}
	if request.Header.Values(credential.Name) != nil {
		return fmt.Errorf("anthropic credential header %q conflicts with a prepared request header", credential.Name)
	}
	request.Header.Set(credential.Name, credential.Value)
	return nil
}

func validateHeaderCredential(credential ai.HeaderCredential) error {
	if strings.TrimSpace(credential.Value) == "" {
		return errors.New("anthropic credential value is empty")
	}
	if err := validateHTTPHeaderName(credential.Name); err != nil {
		return err
	}
	for index := 0; index < len(credential.Value); index++ {
		character := credential.Value[index]
		if character == 0x7f || character < 0x20 && character != '\t' {
			return fmt.Errorf("anthropic credential header %q contains an invalid value", credential.Name)
		}
	}
	return nil
}

func validateHTTPHeaderName(name string) error {
	if name == "" {
		return errors.New("anthropic credential header name is empty")
	}
	for _, character := range name {
		if character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", character) {
			continue
		}
		return fmt.Errorf("anthropic credential header name %q is invalid", name)
	}
	return nil
}

func (c *Client) observeCredentialRejection(
	ctx context.Context,
	request ai.CredentialRequest,
	statusCode int,
) {
	if c.credentialSource == nil ||
		statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden {
		return
	}
	observer, ok := c.credentialSource.(ai.CredentialRejectionObserver)
	if !ok {
		return
	}
	if err := observer.CredentialRejected(ctx, request, statusCode); err != nil {
		if c.Logger == nil {
			return
		}
		errorType, safeError := providers.SanitizedObservationError(err, "callback")
		fields := map[string]interface{}{
			"operation":   "ai_credential_rejection_observer",
			"provider":    "anthropic",
			"status_code": statusCode,
			"error":       safeError.Error(),
			"error_type":  errorType,
		}
		providers.AddObservationRequestID(ctx, fields)
		c.Logger.WarnWithContext(ctx, "Anthropic credential rejection observer failed", fields)
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

func (c *Client) bindRoute(prepared *preparedRequest, route resolvedRoute) {
	if prepared == nil || prepared.Report == nil {
		return
	}
	if route.custom {
		prepared.Report.Adjustments = append(prepared.Report.Adjustments, core.AIRequestAdjustment{
			Source: "endpoint-resolver",
			Rule:   route.identity,
			Path:   "/route",
			Action: "resolve",
			Reason: "trusted endpoint resolution",
		})
		prepared.Adjustments = prepared.Report.Adjustments
	}
	if prepared.Report.Stable && prepared.Report.Fingerprint != "" {
		sum := sha256.Sum256([]byte("policy=" + prepared.Report.Fingerprint + "\nroute=" + route.identity))
		prepared.Report.Fingerprint = fmt.Sprintf("%x", sum[:])
	}
}
