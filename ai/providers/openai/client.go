package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

// Client implements the legacy and request-aware OpenAI client contracts.
type Client struct {
	*providers.BaseClient
	apiKey                   string
	baseURL                  string
	providerAlias            string
	defaultHeaders           map[string]string
	defaultExtra             map[string]interface{}
	ReasoningTokenMultiplier int
	ReasoningEffort          string
	requestPolicy            *requestpolicy.Engine
	credentialSource         ai.CredentialSource
	endpointResolver         ai.EndpointResolver
	requestTimeout           time.Duration
}

// NewClient creates an OpenAI-compatible client with the stock policy engine.
func NewClient(apiKey, baseURL, providerAlias string, logger core.Logger) *Client {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	base := providers.NewBaseClient(180*time.Second, logger)
	base.ProviderName = "openai"
	base.DefaultModel = "default"
	return &Client{
		BaseClient:     base,
		apiKey:         apiKey,
		baseURL:        strings.TrimRight(baseURL, "/"),
		providerAlias:  providerAlias,
		requestPolicy:  newRequestPolicyEngine(),
		requestTimeout: 180 * time.Second,
	}
}

func (c *Client) getProviderName() string {
	if c.providerAlias == "" {
		return "openai"
	}
	return c.providerAlias
}

func truncateForLog(value string, maxLength int) string {
	if len(value) <= maxLength {
		return value
	}
	return value[:maxLength] + "..."
}

func (c *Client) requiresAPIKey() bool { return c.getProviderName() != "openai.ollama" }

func (c *Client) observeError(
	ctx context.Context,
	span core.Span,
	operation string,
	fallback string,
	err error,
) {
	var invocationErr *integrationInvocationError
	if errors.As(err, &invocationErr) {
		switch invocationErr.stage {
		case "credential acquisition", "credential validation":
			fallback = "credential"
		case "transport request":
			fallback = "transport"
		}
	}
	errorType := providers.RecordObservationError(span, err, fallback)
	c.LogErrorMetadata(ctx, providers.ErrorObservation{
		Operation:     operation,
		Provider:      "openai",
		ProviderAlias: c.getProviderName(),
		ErrorType:     errorType,
	})
}

func filterOpenAIExtraFields(
	ctx context.Context,
	logger core.Logger,
	providerAlias string,
	model string,
	caps providers.ModelCapabilities,
	extra map[string]interface{},
) map[string]interface{} {
	if len(extra) == 0 {
		return nil
	}
	filtered := make(map[string]interface{}, len(extra))
	for key, value := range extra {
		switch strings.ToLower(key) {
		case "reasoning":
			if caps.ReasoningStyle != "openai" {
				providers.LogTranslationDegraded(ctx, logger, providerAlias, model, "extra_reasoning_stripped", "reasoning")
				continue
			}
		case "response_format":
			if !caps.SupportsJSONMode {
				providers.LogTranslationDegraded(ctx, logger, providerAlias, model, "extra_response_format_stripped", "response_format")
				continue
			}
		}
		filtered[key] = value
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// GenerateResponse adapts the legacy interface to the request-aware path.
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

// RequestFingerprint returns the stable policy-and-route identity used by
// AI-output caches. Preparation is call-local and does not acquire credentials
// or perform transport I/O.
func (c *Client) RequestFingerprint(ctx context.Context, request *core.AIRequest) (string, bool) {
	prepared, err := c.prepareAIRequest(ctx, request, false)
	if err != nil || prepared == nil || prepared.Report == nil {
		return "", false
	}
	route, err := c.resolveEndpoint(ctx, prepared)
	if err != nil {
		return "", false
	}
	c.bindRoute(prepared, route)
	return prepared.Report.Fingerprint, prepared.Report.Stable && prepared.Report.Fingerprint != ""
}

// Generate executes an OpenAI-compatible chat completion.
func (c *Client) Generate(ctx context.Context, request *core.AIRequest) (*core.AIResult, error) {
	if request == nil {
		return nil, errors.New("OpenAI AI request is nil")
	}
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := c.StartSpan(ctx, "ai.generate_response")
	defer span.End()
	span.SetAttribute("ai.provider", c.getProviderName())
	span.SetAttribute("ai.prompt_length", len(request.Prompt))

	prepared, err := c.prepareAIRequest(ctx, request, false)
	if err != nil {
		if prepared != nil {
			c.recordRequestPreparation(ctx, span, prepared)
			span.SetAttribute("ai.model", prepared.Model)
		}
		c.observeError(ctx, span, "ai_request", "policy", err)
		return resultWithReport(prepared, nil), err
	}
	route, err := c.resolveEndpoint(ctx, prepared)
	if err != nil {
		c.recordRequestPreparation(ctx, span, prepared)
		c.observeError(ctx, span, "ai_request", "route", err)
		return resultWithReport(prepared, nil), err
	}
	c.bindRoute(prepared, route)
	c.recordRequestPreparation(ctx, span, prepared)
	span.SetAttribute("ai.model", prepared.Model)
	span.SetAttribute("ai.request.route_identity", route.identity)
	if c.requiresAPIKey() && c.credentialSource == nil && c.apiKey == "" {
		err := errors.New("OpenAI API key not configured")
		c.observeError(ctx, span, "ai_request", "credential", err)
		return resultWithReport(prepared, nil), err
	}

	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "openai",
		ProviderAlias: c.getProviderName(),
		SemanticModel: prepared.Model,
		PromptLength:  len(request.Prompt),
	})
	started := time.Now()
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		route.url.String(),
		bytes.NewReader(prepared.Body),
	)
	if err != nil {
		c.observeError(ctx, span, "ai_request", "invalid_request", err)
		return resultWithReport(prepared, nil), fmt.Errorf("create OpenAI request: %w", err)
	}
	httpRequest.Header = prepared.Headers.Clone()
	credentialRequest := c.credentialRequest(prepared, route)
	response, err := c.executeWithCredential(ctx, httpRequest, credentialRequest)
	if err != nil {
		c.observeError(ctx, span, "ai_request", "transport", err)
		return resultWithReport(prepared, nil), fmt.Errorf("send OpenAI request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	c.observeCredentialRejection(ctx, credentialRequest, response.StatusCode)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		providerErr := c.HandleError(response.StatusCode, body, "OpenAI", prepared.Model)
		c.observeError(ctx, span, "ai_request", "provider_client", providerErr)
		span.SetAttribute("http.status_code", response.StatusCode)
		return resultWithReport(prepared, nil), providerErr
	}

	result, err := prepared.Codec.Decode(response.Body)
	if err != nil {
		c.observeError(ctx, span, "ai_request", "decode", err)
		return resultWithReport(prepared, nil), err
	}
	result.Response.Provider = c.getProviderName()
	c.recordResponse(ctx, span, prepared.Model, result.Response, started)
	return resultWithReport(prepared, result), nil
}

// StreamResponse adapts the legacy streaming interface to the request-aware path.
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

// Stream executes an OpenAI-compatible server-sent event completion.
func (c *Client) Stream(
	ctx context.Context,
	request *core.AIRequest,
	callback core.StreamCallback,
) (*core.AIResult, error) {
	if request == nil {
		return nil, errors.New("OpenAI AI request is nil")
	}
	if callback == nil {
		return nil, errors.New("OpenAI stream callback is nil")
	}
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := c.StartSpan(ctx, "ai.stream_response")
	defer span.End()
	span.SetAttribute("ai.provider", c.getProviderName())
	span.SetAttribute("ai.streaming", true)
	span.SetAttribute("ai.prompt_length", len(request.Prompt))

	prepared, err := c.prepareAIRequest(ctx, request, true)
	if err != nil {
		if prepared != nil {
			c.recordRequestPreparation(ctx, span, prepared)
			span.SetAttribute("ai.model", prepared.Model)
		}
		c.observeError(ctx, span, "ai_stream", "policy", err)
		return resultWithReport(prepared, nil), err
	}
	route, err := c.resolveEndpoint(ctx, prepared)
	if err != nil {
		c.recordRequestPreparation(ctx, span, prepared)
		c.observeError(ctx, span, "ai_stream", "route", err)
		return resultWithReport(prepared, nil), err
	}
	c.bindRoute(prepared, route)
	c.recordRequestPreparation(ctx, span, prepared)
	span.SetAttribute("ai.model", prepared.Model)
	span.SetAttribute("ai.request.route_identity", route.identity)
	if c.requiresAPIKey() && c.credentialSource == nil && c.apiKey == "" {
		err := errors.New("OpenAI API key not configured")
		c.observeError(ctx, span, "ai_stream", "credential", err)
		return resultWithReport(prepared, nil), err
	}

	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "openai",
		ProviderAlias: c.getProviderName(),
		SemanticModel: prepared.Model,
		PromptLength:  len(request.Prompt),
	})
	started := time.Now()
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		route.url.String(),
		bytes.NewReader(prepared.Body),
	)
	if err != nil {
		c.observeError(ctx, span, "ai_stream", "invalid_request", err)
		return resultWithReport(prepared, nil), fmt.Errorf("create OpenAI stream request: %w", err)
	}
	httpRequest.Header = prepared.Headers.Clone()
	credentialRequest := c.credentialRequest(prepared, route)
	if err := c.prepareCredential(ctx, httpRequest, credentialRequest); err != nil {
		c.observeError(ctx, span, "ai_stream", "credential", err)
		return resultWithReport(prepared, nil), err
	}
	// #nosec G704 -- the request URL comes from validated provider configuration
	// or a trusted EndpointResolver result.
	response, err := c.HTTPClient.Do(httpRequest)
	if err != nil {
		err = &integrationInvocationError{stage: "transport request", cause: err}
		c.observeError(ctx, span, "ai_stream", "transport", err)
		return resultWithReport(prepared, nil), err
	}
	defer func() { _ = response.Body.Close() }()
	c.observeCredentialRejection(ctx, credentialRequest, response.StatusCode)
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		providerErr := c.HandleError(response.StatusCode, body, "OpenAI", prepared.Model)
		c.observeError(ctx, span, "ai_stream", "provider_client", providerErr)
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
	result, err := prepared.Codec.DecodeStream(response.Body, observedCallback)
	if callbackStopped {
		span.SetAttribute("ai.stream_stopped_by_callback", true)
		span.SetAttribute("ai.stream_status", "callback_stop")
	}
	if result != nil && result.Response != nil {
		result.Response.Provider = c.getProviderName()
		c.recordResponse(ctx, span, prepared.Model, result.Response, started)
	}
	if err != nil {
		c.observeError(ctx, span, "ai_stream", "decode", err)
	}
	return resultWithReport(prepared, result), err
}

func (c *Client) recordResponse(
	ctx context.Context,
	span core.Span,
	semanticModel string,
	response *core.AIResponse,
	started time.Time,
) {
	if response == nil {
		return
	}
	span.SetAttribute("ai.prompt_tokens", response.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", response.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", response.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(response.Content))
	span.SetAttribute("ai.duration_ms", time.Since(started).Milliseconds())
	c.LogResponseMetadata(ctx, providers.ResponseObservation{
		Provider:      "openai",
		ProviderAlias: c.getProviderName(),
		SemanticModel: semanticModel,
		Usage:         response.Usage,
		Duration:      time.Since(started),
	})
}

func (c *Client) SupportsStreaming() bool { return true }

var _ core.AIRequestClient = (*Client)(nil)
var _ core.StreamingAIRequestClient = (*Client)(nil)
