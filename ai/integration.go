package ai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// HeaderCredential is one complete HTTP authentication header. Credential
// values are attached after semantic request policy and must never be included
// in request reports, fingerprints, or logs.
type HeaderCredential struct {
	_     noUnkeyedLiterals
	Name  string
	Value string
}

// NewHeaderCredential constructs an HTTP header credential.
func NewHeaderCredential(name, value string) HeaderCredential {
	return HeaderCredential{Name: name, Value: value}
}

// CredentialRequest contains only request identity and trusted routing inputs
// available to a credential source. It is never included in request reports.
type CredentialRequest struct {
	_               noUnkeyedLiterals
	Provider        string
	ProviderAlias   string
	Surface         string
	Operation       string
	ResolvedModel   string
	RouteIdentity   string
	Deployment      string
	CredentialScope string
}

// CredentialSource supplies one credential for each transport attempt. An
// implementation retained by a client must be safe for concurrent use.
type CredentialSource interface {
	Credential(context.Context, CredentialRequest) (HeaderCredential, error)
}

// CredentialRejectionObserver optionally observes provider authentication
// rejections. Observer failures are diagnostic and never replace the original
// provider error.
type CredentialRejectionObserver interface {
	CredentialRejected(context.Context, CredentialRequest, int) error
}

// EndpointRequest contains the sanitized semantic identity available to an
// endpoint resolver.
type EndpointRequest struct {
	_             noUnkeyedLiterals
	Provider      string
	ProviderAlias string
	Surface       string
	ResolvedModel string
	Operation     string
	Purpose       string
}

// EndpointResolver resolves a concrete provider destination after model
// resolution. An implementation retained by a client must be safe for
// concurrent use. AI-output caches may evaluate it during fingerprint
// preflight and again on a cache miss, so implementations must return a stable
// RouteIdentity for the same semantic request and avoid side effects.
type EndpointResolver interface {
	ResolveEndpoint(context.Context, EndpointRequest) (ResolvedEndpoint, error)
}

// AIRequestFailureReason is a bounded provider-local failure classification
// that the chain may safely copy into logs, spans, and metric labels.
type AIRequestFailureReason string

const (
	// AIRequestFailureReasonRoute identifies endpoint resolution or
	// invocation-viability failures that may succeed on another chain entry.
	AIRequestFailureReasonRoute AIRequestFailureReason = "route"
)

// AIRequestFailureReasoner is an optional error contract for provider-local
// failures whose chain meaning cannot be expressed accurately by
// core.ProviderError. The chain accepts only the exported bounded constants;
// unknown values degrade to "unknown".
type AIRequestFailureReasoner interface {
	AIRequestFailureReason() AIRequestFailureReason
}

// ResolvedEndpoint is a trusted route result. URL is the complete request
// endpoint for an HTTP-backed provider. An SDK-native provider may instead
// require URL to be nil and consume Deployment as its opaque SDK destination.
// RouteIdentity must be a stable, non-secret identity suitable for a sanitized
// report and policy fingerprint. Query and CredentialScope are never reported.
// Deployment is likewise excluded from reports.
type ResolvedEndpoint struct {
	_               noUnkeyedLiterals
	URL             *url.URL
	Deployment      string
	RouteIdentity   string
	CredentialScope string
	Query           url.Values
}

// AuthHeaderFunc returns one complete authentication header value. It must be
// safe for concurrent use.
type AuthHeaderFunc func(context.Context) (string, error)

type callbackCredentialSource struct {
	name  string
	value AuthHeaderFunc
}

func (source callbackCredentialSource) Credential(ctx context.Context, _ CredentialRequest) (HeaderCredential, error) {
	value, err := source.value(ctx)
	if err != nil {
		return HeaderCredential{}, fmt.Errorf("auth header callback failed: %w", err)
	}
	if strings.TrimSpace(value) == "" {
		return HeaderCredential{}, errors.New("auth header callback returned an empty value")
	}
	if err := validateIntegrationHeaderValue(source.name, value); err != nil {
		return HeaderCredential{}, err
	}
	return NewHeaderCredential(source.name, value), nil
}

// WithCredentialSource sets the dynamic credential source. When supplied, it
// takes precedence over the provider's static API-key configuration.
func WithCredentialSource(source CredentialSource) ClientOption {
	return clientOptionFunc(func(config *clientConfig) error {
		config.integration.CredentialSource = source
		config.credentialSourceSet = true
		return nil
	})
}

// WithEndpointResolver sets trusted per-request endpoint resolution.
func WithEndpointResolver(resolver EndpointResolver) ClientOption {
	return clientOptionFunc(func(config *clientConfig) error {
		config.integration.EndpointResolver = resolver
		config.endpointResolverSet = true
		return nil
	})
}

// WithAuthHeader adapts a dynamic header callback to CredentialSource.
func WithAuthHeader(name string, value AuthHeaderFunc) ClientOption {
	validationErr := validateIntegrationHeaderName(name)
	if value == nil && validationErr == nil {
		validationErr = errors.New("auth header callback is nil")
	}
	return clientOptionFunc(func(config *clientConfig) error {
		if validationErr != nil {
			return validationErr
		}
		config.integration.CredentialSource = callbackCredentialSource{name: name, value: value}
		config.credentialSourceSet = true
		return nil
	})
}

// WithHTTPClient injects a caller-owned HTTP client. Supporting providers
// shallow-copy the client and never mutate the supplied value.
func WithHTTPClient(client *http.Client) ClientOption {
	return clientOptionFunc(func(config *clientConfig) error {
		config.integration.HTTPClient = client
		config.httpClientSet = true
		return nil
	})
}

func validateIntegrationHeaderValue(name, value string) error {
	if err := validateIntegrationHeaderName(name); err != nil {
		return err
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == 0x7f || character < 0x20 && character != '\t' {
			return fmt.Errorf("header %q contains an invalid value", name)
		}
	}
	return nil
}

func validateIntegrationHeaderName(name string) error {
	if name == "" {
		return errors.New("auth header name is empty")
	}
	for _, character := range name {
		if !isHTTPTokenCharacter(character) {
			return fmt.Errorf("auth header name %q is invalid", name)
		}
	}
	return nil
}

func isHTTPTokenCharacter(character rune) bool {
	if character >= 'a' && character <= 'z' ||
		character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' {
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", character)
}
