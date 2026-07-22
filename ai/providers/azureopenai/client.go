package azureopenai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

// Client executes one exact Azure OpenAI Chat Completions surface.
type Client struct {
	*providers.BaseClient
	providerAlias          string
	surface                surface
	staticAPIKey           string
	defaultHeaders         map[string]string
	defaultExtra           map[string]interface{}
	defaultReasoningEffort string
	requestPolicy          *requestpolicy.Engine
	credentialSource       ai.CredentialSource
	endpointResolver       ai.EndpointResolver
	codec                  openaiwire.ProfiledCodec
	requestTimeout         time.Duration
}

func newClient(
	config *ai.AIConfig,
	integration ai.ProviderIntegrationConfig,
	selected surface,
) (*Client, error) {
	version, err := selected.surfaceVersion()
	if err != nil {
		return nil, err
	}
	codec, err := openaiwire.NewProfiledCodec(openaiwire.Config{
		SurfaceVersion:           version,
		ReasoningTokenMultiplier: config.ReasoningTokenMultiplier,
		DefaultReasoningEffort:   config.ReasoningEffort,
	})
	if err != nil {
		return nil, fmt.Errorf("create Azure OpenAI wire codec: %w", err)
	}
	engine, err := requestpolicy.NewEngine(requestpolicy.Config{
		AppRules: integration.RequestRules, Middleware: integration.RequestMiddleware,
		Mode: integration.CompatibilityMode,
	})
	if err != nil {
		return nil, fmt.Errorf("configure Azure OpenAI request policy: %w", err)
	}
	logger := config.Logger
	if logger == nil {
		logger = &core.NoOpLogger{}
	} else if componentLogger, ok := logger.(core.ComponentAwareLogger); ok {
		logger = componentLogger.WithComponent("framework/ai")
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	base := providers.NewBaseClient(timeout, logger)
	base.ProviderName = providerName
	base.DefaultModel = "default"
	if config.Model != "" {
		base.DefaultModel = config.Model
	}
	if config.Temperature > 0 {
		base.DefaultTemperature = config.Temperature
	}
	if config.MaxTokens > 0 {
		base.DefaultMaxTokens = config.MaxTokens
	}
	if config.MaxRetries >= 0 {
		base.MaxRetries = config.MaxRetries
	}
	if config.Telemetry != nil {
		base.SetTelemetry(config.Telemetry)
	}
	if integration.HTTPClient != nil {
		base.HTTPClient = providerHTTPClient(integration.HTTPClient)
	}
	client := &Client{
		BaseClient: base, providerAlias: config.ProviderAlias, surface: selected,
		staticAPIKey: config.APIKey, defaultReasoningEffort: config.ReasoningEffort,
		requestPolicy: engine, credentialSource: integration.CredentialSource,
		endpointResolver: integration.EndpointResolver, codec: codec, requestTimeout: timeout,
	}
	if len(config.Headers) > 0 {
		client.defaultHeaders = providers.MergeStringMaps(nil, config.Headers)
	}
	if len(config.Extra) > 0 {
		cloned, err := providers.CloneAIOptions(&core.AIOptions{Extra: config.Extra})
		if err != nil {
			return nil, fmt.Errorf("clone Azure OpenAI default request extras: %w", err)
		}
		client.defaultExtra = cloned.Extra
	}
	logger.Info("Azure OpenAI provider initialized", map[string]interface{}{
		"operation": "ai_provider_init", "provider": providerName,
		"provider_alias":     config.ProviderAlias,
		"has_static_api_key": config.APIKey != "", "has_credential_source": integration.CredentialSource != nil,
		"timeout": timeout.String(), "max_retries": base.MaxRetries, "model": base.DefaultModel,
	})
	return client, nil
}

func (c *Client) observeError(
	ctx context.Context,
	span core.Span,
	operation string,
	err error,
	duration time.Duration,
) {
	fallback := "provider_client"
	var invocationErr *integrationInvocationError
	if errors.As(err, &invocationErr) {
		switch invocationErr.stage {
		case "endpoint resolution":
			fallback = "route"
		case "credential acquisition":
			fallback = "credential"
		case "transport request":
			fallback = "transport"
		}
	}
	var policyErr *requestpolicy.PolicyError
	var featureErr *core.AIRequestFeatureError
	if errors.As(err, &policyErr) {
		fallback = "policy"
	} else if errors.As(err, &featureErr) {
		fallback = "invalid_request"
	}
	errorType := providers.RecordObservationError(span, err, fallback)
	c.LogErrorMetadata(ctx, providers.ErrorObservation{
		Operation: operation, Provider: providerName, ProviderAlias: c.providerAlias,
		ErrorType: errorType, Duration: duration,
	})
}

// GenerateResponse adapts the legacy interface to the supported request path.
func (c *Client) GenerateResponse(
	ctx context.Context,
	prompt string,
	options *core.AIOptions,
) (*core.AIResponse, error) {
	result, err := c.Generate(ctx, core.NewAIRequestFromLegacy(prompt, "", options))
	if result != nil && result.Response != nil {
		return result.Response, err
	}
	return nil, err
}

// RequestFingerprint returns the stable policy-and-route identity.
func (c *Client) RequestFingerprint(ctx context.Context, request *core.AIRequest) (string, bool) {
	invocation, err := c.prepareInvocation(ctx, request, false)
	if err != nil || invocation == nil || invocation.Request == nil || invocation.Request.Report == nil {
		return "", false
	}
	report := invocation.Request.Report
	return report.Fingerprint, report.Stable && report.Fingerprint != ""
}

// Generate executes one Azure OpenAI chat completion.
func (c *Client) Generate(ctx context.Context, request *core.AIRequest) (result *core.AIResult, err error) {
	started := time.Now()
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := c.StartSpan(ctx, "ai.generate_response")
	defer func() { c.finishProviderSpan(ctx, span, "ai_request", started, result, err) }()
	span.SetAttribute("ai.provider", providerName)
	span.SetAttribute("ai.provider_alias", c.providerAlias)
	if request == nil {
		return nil, errors.New("azure OpenAI request is nil")
	}
	span.SetAttribute("ai.prompt_length", len(request.Prompt))
	invocation, err := c.prepareInvocation(ctx, request, false)
	if err != nil {
		prepared := preparedFromInvocation(invocation)
		c.recordRequestPreparation(ctx, span, prepared)
		return resultWithReport(prepared, nil), err
	}
	prepared, route := invocation.Request, invocation.Route
	c.recordRequestPreparation(ctx, span, prepared)
	span.SetAttribute("ai.model", prepared.SemanticModel)
	span.SetAttribute("ai.surface", prepared.Report.Surface)
	span.SetAttribute("ai.request.route_identity", route.identity)
	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider: providerName, ProviderAlias: c.providerAlias,
		SemanticModel: prepared.SemanticModel, PromptLength: len(request.Prompt),
	})
	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, route.url.String(), bytes.NewReader(prepared.Body),
	)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("create Azure OpenAI request: %w", err)
	}
	httpRequest.Header = prepared.Headers.Clone()
	credentialRequest := c.credentialRequest(prepared, route)
	response, err := c.ExecuteWithRetryPrepared(
		ctx, httpRequest,
		func(attemptCtx context.Context, attempt *http.Request) error {
			return c.prepareCredential(attemptCtx, attempt, credentialRequest)
		},
	)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("send Azure OpenAI request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	c.observeCredentialRejection(ctx, credentialRequest, response.StatusCode)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		providerErr := c.HandleError(response.StatusCode, body, "Azure OpenAI", prepared.SemanticModel)
		span.SetAttribute("http.status_code", response.StatusCode)
		return resultWithReport(prepared, nil), providerErr
	}
	result, err = c.decode(response.Body)
	if err != nil {
		return resultWithReport(prepared, result), err
	}
	c.recordResponse(ctx, prepared.SemanticModel, result.Response, started)
	return resultWithReport(prepared, result), nil
}

// StreamResponse adapts the legacy streaming interface to the request path.
func (c *Client) StreamResponse(
	ctx context.Context,
	prompt string,
	options *core.AIOptions,
	callback core.StreamCallback,
) (*core.AIResponse, error) {
	result, err := c.Stream(ctx, core.NewAIRequestFromLegacy(prompt, "", options), callback)
	if result != nil && result.Response != nil {
		return result.Response, err
	}
	return nil, err
}

// Stream executes one Azure OpenAI server-sent event completion.
func (c *Client) Stream(
	ctx context.Context,
	request *core.AIRequest,
	callback core.StreamCallback,
) (result *core.AIResult, err error) {
	started := time.Now()
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := c.StartSpan(ctx, "ai.stream_response")
	defer func() { c.finishProviderSpan(ctx, span, "ai_stream", started, result, err) }()
	span.SetAttribute("ai.provider", providerName)
	span.SetAttribute("ai.provider_alias", c.providerAlias)
	span.SetAttribute("ai.streaming", true)
	if request == nil {
		return nil, errors.New("azure OpenAI request is nil")
	}
	if callback == nil {
		return nil, errors.New("azure OpenAI callback is nil")
	}
	span.SetAttribute("ai.prompt_length", len(request.Prompt))
	invocation, err := c.prepareInvocation(ctx, request, true)
	if err != nil {
		prepared := preparedFromInvocation(invocation)
		c.recordRequestPreparation(ctx, span, prepared)
		return resultWithReport(prepared, nil), err
	}
	prepared, route := invocation.Request, invocation.Route
	c.recordRequestPreparation(ctx, span, prepared)
	span.SetAttribute("ai.model", prepared.SemanticModel)
	span.SetAttribute("ai.surface", prepared.Report.Surface)
	span.SetAttribute("ai.request.route_identity", route.identity)
	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider: providerName, ProviderAlias: c.providerAlias,
		SemanticModel: prepared.SemanticModel, PromptLength: len(request.Prompt),
	})
	httpRequest, err := http.NewRequestWithContext(
		ctx, http.MethodPost, route.url.String(), bytes.NewReader(prepared.Body),
	)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("create Azure OpenAI stream request: %w", err)
	}
	httpRequest.Header = prepared.Headers.Clone()
	credentialRequest := c.credentialRequest(prepared, route)
	response, err := c.ExecuteWithRetryPrepared(
		ctx, httpRequest,
		func(attemptCtx context.Context, attempt *http.Request) error {
			return c.prepareCredential(attemptCtx, attempt, credentialRequest)
		},
	)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("send Azure OpenAI stream request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	c.observeCredentialRejection(ctx, credentialRequest, response.StatusCode)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		providerErr := c.HandleError(response.StatusCode, body, "Azure OpenAI", prepared.SemanticModel)
		span.SetAttribute("http.status_code", response.StatusCode)
		return resultWithReport(prepared, nil), providerErr
	}
	callbackStopped := false
	observedCallback := func(chunk core.StreamChunk) error {
		callbackErr := callback(chunk)
		if callbackErr != nil {
			callbackStopped = true
		}
		return callbackErr
	}
	result, err = c.decodeStream(response.Body, observedCallback)
	if callbackStopped {
		span.SetAttribute("ai.stream_stopped_by_callback", true)
		span.SetAttribute("ai.stream_status", "callback_stop")
	}
	if result != nil && result.Response != nil {
		c.recordResponse(ctx, prepared.SemanticModel, result.Response, started)
	}
	return resultWithReport(prepared, result), err
}

func (c *Client) decode(response io.Reader) (*core.AIResult, error) {
	result, err := c.codec.Decode(response)
	return c.bindResultIdentity(result), err
}

func (c *Client) decodeStream(response io.Reader, callback core.StreamCallback) (*core.AIResult, error) {
	result, err := c.codec.DecodeStream(response, callback)
	return c.bindResultIdentity(result), err
}

func (c *Client) bindResultIdentity(result *core.AIResult) *core.AIResult {
	if result != nil && result.Response != nil {
		result.Response.Provider = c.providerAlias
	}
	return result
}

func (c *Client) recordResponse(
	ctx context.Context,
	semanticModel string,
	response *core.AIResponse,
	started time.Time,
) {
	if response == nil {
		return
	}
	c.LogResponseMetadata(ctx, providers.ResponseObservation{
		Provider: providerName, ProviderAlias: c.providerAlias,
		SemanticModel: semanticModel, Usage: response.Usage, Duration: time.Since(started),
	})
}

func preparedFromInvocation(invocation *preparedInvocation) *preparedRequest {
	if invocation == nil {
		return nil
	}
	return invocation.Request
}

func (c *Client) finishProviderSpan(
	ctx context.Context,
	span core.Span,
	operation string,
	started time.Time,
	result *core.AIResult,
	err error,
) {
	defer span.End()
	duration := time.Since(started)
	span.SetAttribute("ai.duration_ms", duration.Milliseconds())
	if err != nil {
		span.SetAttribute("ai.status", "error")
		c.observeError(ctx, span, operation, err, duration)
		return
	}
	span.SetAttribute("ai.status", "success")
	if result == nil || result.Response == nil {
		return
	}
	span.SetAttribute("ai.prompt_tokens", result.Response.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", result.Response.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", result.Response.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(result.Response.Content))
}

// SupportsStreaming reports native SSE support.
func (*Client) SupportsStreaming() bool { return true }

var _ core.AIClient = (*Client)(nil)
var _ core.AIRequestClient = (*Client)(nil)
var _ core.StreamingAIClient = (*Client)(nil)
var _ core.StreamingAIRequestClient = (*Client)(nil)
var _ core.AIRequestFingerprinter = (*Client)(nil)
