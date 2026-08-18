package gemini

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

const defaultRouteIdentity = "gemini.generate-content.v1beta.default"

var geminiModelIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

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

func (err *integrationInvocationError) Error() string {
	return fmt.Sprintf("Gemini %s failed", err.stage)
}

func (err *integrationInvocationError) Unwrap() error { return err.cause }

func (client *Client) withRequestTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if client.requestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, client.requestTimeout)
}

func (client *Client) resolveEndpoint(ctx context.Context, request ai.EndpointRequest) (resolvedRoute, error) {
	if client.endpointResolver == nil {
		path, err := selectedWireProfile.endpointPath(request.ResolvedModel, request.Operation == "stream")
		if err != nil {
			return resolvedRoute{}, err
		}
		endpoint, err := parseGeminiEndpoint(strings.TrimRight(client.baseURL, "/") + path)
		if err != nil {
			return resolvedRoute{}, err
		}
		return resolvedRoute{url: endpoint, identity: defaultRouteIdentity}, nil
	}

	resolved, err := client.endpointResolver.ResolveEndpoint(ctx, request)
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
		custom:          true,
	}
	if err := validateGeminiRoute(route, request.Operation); err != nil {
		return resolvedRoute{}, err
	}
	return route, nil
}

func (profile wireProfile) endpointPath(model string, stream bool) (string, error) {
	if profile != selectedWireProfile {
		return "", errors.New("unsupported Gemini wire profile")
	}
	if !geminiModelIDPattern.MatchString(model) {
		return "", errors.New("gemini model ID is invalid")
	}
	method := "generateContent"
	query := ""
	if stream {
		method = "streamGenerateContent"
		query = "?alt=sse"
	}
	return "/models/" + model + ":" + method + query, nil
}

func snapshotResolvedEndpoint(resolved ai.ResolvedEndpoint) (ai.ResolvedEndpoint, error) {
	if resolved.URL == nil {
		return ai.ResolvedEndpoint{}, errors.New("gemini resolved endpoint URL is nil")
	}
	if err := validateRouteIdentity(resolved.RouteIdentity); err != nil {
		return ai.ResolvedEndpoint{}, err
	}
	clonedURL := *resolved.URL
	if _, err := parseGeminiEndpoint(clonedURL.String()); err != nil {
		return ai.ResolvedEndpoint{}, err
	}
	query := clonedURL.Query()
	for key, values := range resolved.Query {
		query[key] = append([]string(nil), values...)
	}
	if query.Has("key") || query.Has("x-goog-api-key") {
		return ai.ResolvedEndpoint{}, errors.New("gemini endpoint query must not contain credentials")
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

func parseGeminiEndpoint(rawURL string) (*url.URL, error) {
	endpoint, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("gemini endpoint URL is invalid")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("unsupported Gemini endpoint scheme %q", endpoint.Scheme)
	}
	if endpoint.Host == "" {
		return nil, errors.New("gemini endpoint host is empty")
	}
	if endpoint.User != nil {
		return nil, errors.New("gemini endpoint must not contain user information")
	}
	if endpoint.Fragment != "" {
		return nil, errors.New("gemini endpoint must not contain a fragment")
	}
	if endpoint.Path == "" || endpoint.Path[0] != '/' {
		return nil, errors.New("gemini endpoint path is invalid")
	}
	if endpoint.Query().Has("key") || endpoint.Query().Has("x-goog-api-key") {
		return nil, errors.New("gemini endpoint query must not contain credentials")
	}
	clone := *endpoint
	return &clone, nil
}

func validateGeminiBaseURL(rawURL string) error {
	endpoint, err := parseGeminiEndpoint(strings.TrimRight(rawURL, "/") + "/models/model:generateContent")
	if err != nil {
		return err
	}
	if endpoint.RawQuery != "" {
		return errors.New("gemini base URL must not contain query parameters")
	}
	return nil
}

func validateGeminiRoute(route resolvedRoute, operation string) error {
	if route.url == nil {
		return errors.New("gemini route URL is nil")
	}
	method := "generateContent"
	if operation == "stream" {
		method = "streamGenerateContent"
	} else if operation != "generate" {
		return fmt.Errorf("unsupported Gemini operation %q", operation)
	}
	pattern := regexp.MustCompile(`/models/([A-Za-z0-9._-]+):` + method + `$`)
	match := pattern.FindStringSubmatch(route.url.Path)
	if len(match) != 2 {
		return errors.New("gemini endpoint method does not match the operation")
	}
	if route.deployment != "" && route.deployment != match[1] {
		return errors.New("gemini route deployment does not match its URL")
	}
	if operation == "stream" {
		if values := route.url.Query()["alt"]; len(values) != 1 || values[0] != "sse" {
			return errors.New("gemini streaming endpoint must select alt=sse exactly once")
		}
	} else if route.url.Query().Has("alt") {
		return errors.New("gemini buffered endpoint must not select an alternate response format")
	}
	return nil
}

func validateRouteIdentity(identity string) error {
	if strings.TrimSpace(identity) == "" {
		return errors.New("gemini resolved endpoint route identity is empty")
	}
	if len(identity) > 256 {
		return errors.New("gemini resolved endpoint route identity exceeds 256 bytes")
	}
	for _, character := range identity {
		if character == 0x7f || character < 0x20 {
			return errors.New("gemini resolved endpoint route identity contains control characters")
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

func (client *Client) credentialRequest(prepared *preparedRequest, route resolvedRoute) ai.CredentialRequest {
	request := ai.CredentialRequest{
		Provider:        "gemini",
		ProviderAlias:   "gemini",
		Surface:         selectedWireProfile.surface,
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

func (client *Client) executeWithCredential(
	ctx context.Context,
	request *http.Request,
	credentialRequest ai.CredentialRequest,
) (*http.Response, error) {
	return client.ExecuteWithRetryPrepared(ctx, request, func(attemptCtx context.Context, attempt *http.Request) error {
		return client.prepareCredential(attemptCtx, attempt, credentialRequest)
	})
}

func (client *Client) prepareCredential(
	ctx context.Context,
	request *http.Request,
	credentialRequest ai.CredentialRequest,
) error {
	credential := ai.NewHeaderCredential("x-goog-api-key", client.apiKey)
	if client.credentialSource != nil {
		resolved, err := client.credentialSource.Credential(ctx, credentialRequest)
		if err != nil {
			return &integrationInvocationError{stage: "credential acquisition", cause: err}
		}
		credential = resolved
	}
	if err := validateGeminiCredential(credential); err != nil {
		return &integrationInvocationError{stage: "credential validation", cause: err}
	}
	if request.Header.Values("x-goog-api-key") != nil {
		return errors.New("gemini credential conflicts with a prepared request header")
	}
	request.Header.Set("x-goog-api-key", credential.Value)
	return nil
}

func validateGeminiCredential(credential ai.HeaderCredential) error {
	if !strings.EqualFold(credential.Name, "x-goog-api-key") {
		return errors.New("gemini credential must use x-goog-api-key")
	}
	if strings.TrimSpace(credential.Value) == "" {
		return errors.New("gemini credential value is empty")
	}
	for index := 0; index < len(credential.Value); index++ {
		character := credential.Value[index]
		if character == 0x7f || character < 0x20 && character != '\t' {
			return errors.New("gemini credential contains an invalid value")
		}
	}
	return nil
}

func (client *Client) observeCredentialRejection(
	ctx context.Context,
	request ai.CredentialRequest,
	statusCode int,
) {
	if client.credentialSource == nil ||
		statusCode != http.StatusUnauthorized && statusCode != http.StatusForbidden {
		return
	}
	observer, ok := client.credentialSource.(ai.CredentialRejectionObserver)
	if !ok {
		return
	}
	if err := observer.CredentialRejected(ctx, request, statusCode); err != nil && client.Logger != nil {
		errorType, safeError := providers.SanitizedObservationError(err, "callback")
		fields := map[string]interface{}{
			"operation":   "ai_credential_rejection_observer",
			"provider":    "gemini",
			"status_code": statusCode,
			"error":       safeError.Error(),
			"error_type":  errorType,
		}
		providers.AddObservationRequestID(ctx, fields)
		client.Logger.WarnWithContext(ctx, "Gemini credential rejection observer failed", fields)
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

func (client *Client) bindRoute(prepared *preparedRequest, route resolvedRoute) {
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
	}
	if prepared.Report.Stable && prepared.Report.Fingerprint != "" {
		sum := sha256.Sum256([]byte("policy=" + prepared.Report.Fingerprint + "\nroute=" + route.identity))
		prepared.Report.Fingerprint = fmt.Sprintf("%x", sum[:])
	}
}
