package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/ai/providerkit/openaiwire"
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
	sseEventMaxBytes         int
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
		BaseClient:       base,
		apiKey:           apiKey,
		baseURL:          strings.TrimRight(baseURL, "/"),
		providerAlias:    providerAlias,
		requestPolicy:    newRequestPolicyEngine(),
		requestTimeout:   180 * time.Second,
		sseEventMaxBytes: openaiwire.DefaultMaxSSEEventBytes,
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
	duration time.Duration,
) {
	var invocationErr *integrationInvocationError
	if errors.As(err, &invocationErr) {
		switch invocationErr.stage {
		case "endpoint resolution":
			fallback = "route"
		case "credential acquisition", "credential validation":
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
		Operation:     operation,
		Provider:      "openai",
		ProviderAlias: c.getProviderName(),
		ErrorType:     errorType,
		Duration:      duration,
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
			allow := providerAlias == openRouterProviderAlias ||
				(providerAlias == "openai.ollama" && caps.ReasoningStyle == "openai")
			if !allow {
				providers.LogTranslationDegraded(ctx, logger, providerAlias, model, "extra_reasoning_stripped", "reasoning")
				continue
			}
		case "reasoning_effort":
			if providerAlias == openRouterProviderAlias || caps.ReasoningStyle != "openai" || providerAlias == "openai.ollama" {
				providers.LogTranslationDegraded(ctx, logger, providerAlias, model, "extra_reasoning_effort_stripped", "reasoning_effort")
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
	invocation, err := c.prepareInvocation(ctx, request, false)
	if err != nil || invocation == nil || invocation.Request == nil || invocation.Request.Report == nil {
		return "", false
	}
	report := invocation.Request.Report
	return report.Fingerprint, report.Stable && report.Fingerprint != ""
}

// Generate executes an OpenAI-compatible chat completion.
func (c *Client) Generate(ctx context.Context, request *core.AIRequest) (result *core.AIResult, err error) {
	started := time.Now()
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := c.StartSpan(ctx, "ai.generate_response")
	defer func() { c.finishProviderSpan(ctx, span, "ai_request", started, result, err) }()
	span.SetAttribute("ai.provider", "openai")
	span.SetAttribute("ai.provider_alias", c.getProviderName())
	if request == nil {
		return nil, errors.New("OpenAI AI request is nil")
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
	span.SetAttribute("ai.model", prepared.Model)
	span.SetAttribute("ai.surface", prepared.Report.Surface)
	span.SetAttribute("ai.request.route_identity", route.identity)
	if c.requiresAPIKey() && c.credentialSource == nil && c.apiKey == "" {
		return resultWithReport(prepared, nil), errors.New("OpenAI API key not configured")
	}

	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "openai",
		ProviderAlias: c.getProviderName(),
		SemanticModel: prepared.Model,
		PromptLength:  len(request.Prompt),
	})
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		route.url.String(),
		bytes.NewReader(prepared.Body),
	)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("create OpenAI request: %w", err)
	}
	httpRequest.Header = prepared.Headers.Clone()
	credentialRequest := c.credentialRequest(prepared, route)
	response, err := c.executeWithCredential(ctx, httpRequest, credentialRequest)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("send OpenAI request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	providerRequestID := c.observeProviderRequestID(span, response.Header.Get("X-Generation-Id"))
	c.observeCredentialRejection(ctx, credentialRequest, response.StatusCode)
	if response.StatusCode != http.StatusOK {
		body, _ := providers.ReadErrorBody(response.Body)
		providerErr := c.compatibleHTTPError(response.StatusCode, body, prepared.Model)
		span.SetAttribute("http.status_code", response.StatusCode)
		return resultWithReport(prepared, nil), providerErr
	}

	result, err = prepared.Codec.Decode(response.Body)
	if result != nil && result.Response != nil {
		result.Response.Provider = c.getProviderName()
		c.observeResponseIdentity(span, result.Response, providerRequestID)
	}
	if err != nil {
		return resultWithReport(prepared, result), c.normalizeCompatibleError(err, prepared.Model)
	}
	c.recordSuccessfulResponse(ctx, span, prepared.Model, result.Response, providerRequestID, started)
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
) (result *core.AIResult, err error) {
	started := time.Now()
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := c.StartSpan(ctx, "ai.stream_response")
	defer func() { c.finishProviderSpan(ctx, span, "ai_stream", started, result, err) }()
	span.SetAttribute("ai.provider", "openai")
	span.SetAttribute("ai.provider_alias", c.getProviderName())
	span.SetAttribute("ai.streaming", true)
	if request == nil {
		return nil, errors.New("OpenAI AI request is nil")
	}
	if callback == nil {
		return nil, errors.New("OpenAI stream callback is nil")
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
	span.SetAttribute("ai.model", prepared.Model)
	span.SetAttribute("ai.surface", prepared.Report.Surface)
	span.SetAttribute("ai.request.route_identity", route.identity)
	if c.requiresAPIKey() && c.credentialSource == nil && c.apiKey == "" {
		return resultWithReport(prepared, nil), errors.New("OpenAI API key not configured")
	}

	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "openai",
		ProviderAlias: c.getProviderName(),
		SemanticModel: prepared.Model,
		PromptLength:  len(request.Prompt),
	})
	httpRequest, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		route.url.String(),
		bytes.NewReader(prepared.Body),
	)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("create OpenAI stream request: %w", err)
	}
	httpRequest.Header = prepared.Headers.Clone()
	credentialRequest := c.credentialRequest(prepared, route)
	response, err := c.executeWithCredential(ctx, httpRequest, credentialRequest)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("send OpenAI stream request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	providerRequestID := c.observeProviderRequestID(span, response.Header.Get("X-Generation-Id"))
	c.observeCredentialRejection(ctx, credentialRequest, response.StatusCode)
	if response.StatusCode != http.StatusOK {
		body, _ := providers.ReadErrorBody(response.Body)
		providerErr := c.compatibleHTTPError(response.StatusCode, body, prepared.Model)
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
	result, err = prepared.Codec.DecodeStream(response.Body, observedCallback)
	if callbackStopped {
		span.SetAttribute("ai.stream_stopped_by_callback", true)
		span.SetAttribute("ai.stream_status", "callback_stop")
	}
	if result != nil && result.Response != nil {
		result.Response.Provider = c.getProviderName()
		c.observeResponseIdentity(span, result.Response, providerRequestID)
		if err == nil {
			c.recordSuccessfulResponse(ctx, span, prepared.Model, result.Response, providerRequestID, started)
		}
	}
	if err != nil {
		normalized := c.normalizeCompatibleError(err, prepared.Model)
		if errors.Is(err, core.ErrStreamPartiallyCompleted) {
			normalized = errors.Join(core.ErrStreamPartiallyCompleted, normalized)
		}
		err = normalized
	}
	return resultWithReport(prepared, result), err
}

func (c *Client) compatibleHTTPError(status int, body []byte, model string) error {
	if c.getProviderName() == openRouterProviderAlias {
		return normalizeOpenRouterHTTPError(status, body, model)
	}
	return c.HandleError(status, body, "OpenAI", model)
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
	span.SetAttribute("ai.duration_ms", time.Since(started).Milliseconds())
	if err != nil {
		span.SetAttribute("ai.status", "error")
		c.observeError(ctx, span, operation, "provider_client", err, time.Since(started))
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

const (
	maxOpenRouterGenerationIDBytes  = 128
	maxOpenRouterResponseModelBytes = 256
)

var (
	openRouterGenerationIDPattern  = regexp.MustCompile(`^gen-[A-Za-z0-9_-]+$`)
	openRouterResponseModelPattern = regexp.MustCompile(`^~?[A-Za-z0-9][A-Za-z0-9._+-]*/[A-Za-z0-9][A-Za-z0-9._~:+-]*$`)
)

func sanitizeGenerationID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > maxOpenRouterGenerationIDBytes || !openRouterGenerationIDPattern.MatchString(value) {
		return ""
	}
	return value
}

func sanitizeResponseModel(value string) string {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > maxOpenRouterResponseModelBytes || !openRouterResponseModelPattern.MatchString(value) {
		return ""
	}
	return value
}

func (c *Client) observeProviderRequestID(span core.Span, value string) string {
	if c.getProviderName() != openRouterProviderAlias {
		return ""
	}
	sanitized := sanitizeGenerationID(value)
	if sanitized != "" {
		span.SetAttribute("ai.provider_request_id", sanitized)
	}
	return sanitized
}

func (c *Client) observeResponseIdentity(
	span core.Span,
	response *core.AIResponse,
	providerRequestID string,
) {
	if c.getProviderName() != openRouterProviderAlias || response == nil {
		return
	}
	if responseModel := sanitizeResponseModel(response.Model); responseModel != "" {
		span.SetAttribute("ai.response.model", responseModel)
	}
	if providerRequestID != "" {
		span.SetAttribute("ai.provider_request_id", providerRequestID)
	}
}

func (c *Client) recordSuccessfulResponse(
	ctx context.Context,
	span core.Span,
	semanticModel string,
	response *core.AIResponse,
	providerRequestID string,
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
	observation := providers.ResponseObservation{
		Provider:      "openai",
		ProviderAlias: c.getProviderName(),
		SemanticModel: semanticModel,
		Usage:         response.Usage,
		Duration:      time.Since(started),
	}
	if c.getProviderName() == openRouterProviderAlias {
		observation.ResponseModel = sanitizeResponseModel(response.Model)
		observation.ProviderRequestID = providerRequestID
	}
	c.LogResponseMetadata(ctx, observation)
}

func (c *Client) SupportsStreaming() bool { return true }

var _ core.AIRequestClient = (*Client)(nil)
var _ core.StreamingAIRequestClient = (*Client)(nil)
