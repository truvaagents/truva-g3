package gemini

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

const DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// Client implements both the legacy and request-aware Gemini contracts.
type Client struct {
	*providers.BaseClient
	apiKey           string
	baseURL          string
	defaultHeaders   map[string]string
	defaultExtra     map[string]interface{}
	defaultReasoning string
	requestPolicy    *requestpolicy.Engine
	credentialSource ai.CredentialSource
	endpointResolver ai.EndpointResolver
	requestTimeout   time.Duration
}

func NewClient(apiKey, baseURL string, logger core.Logger) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	const timeout = 180 * time.Second
	base := providers.NewBaseClient(timeout, logger)
	base.ProviderName = "gemini"
	base.DefaultModel = "default"
	base.DefaultMaxTokens = 1000
	return &Client{
		BaseClient:     base,
		apiKey:         apiKey,
		baseURL:        baseURL,
		requestPolicy:  newRequestPolicyEngine(),
		requestTimeout: timeout,
	}
}

func (client *Client) observeError(
	ctx context.Context,
	span core.Span,
	operation string,
	fallback string,
	err error,
) {
	client.observeErrorWithDuration(ctx, span, operation, fallback, err, 0)
}

func (client *Client) observeErrorWithDuration(
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
	client.LogErrorMetadata(ctx, providers.ErrorObservation{
		Operation:     operation,
		Provider:      "gemini",
		ProviderAlias: "gemini",
		ErrorType:     errorType,
		Duration:      duration,
	})
}

// GenerateResponse preserves the legacy AIClient surface by adapting it to the
// same request-aware preparation and execution path as Generate.
func (client *Client) GenerateResponse(
	ctx context.Context,
	prompt string,
	options *core.AIOptions,
) (*core.AIResponse, error) {
	result, err := client.Generate(ctx, core.NewAIRequestFromLegacy(prompt, "", options))
	if result != nil && result.Response != nil {
		return result.Response, err
	}
	return nil, err
}

// RequestFingerprint returns the stable policy-and-route identity without
// acquiring credentials or performing transport I/O.
func (client *Client) RequestFingerprint(ctx context.Context, request *core.AIRequest) (string, bool) {
	invocation, err := client.prepareInvocation(ctx, request, false)
	if err != nil || invocation == nil || invocation.Request == nil || invocation.Request.Report == nil {
		return "", false
	}
	report := invocation.Request.Report
	return report.Fingerprint, report.Stable && report.Fingerprint != ""
}

func (client *Client) Generate(
	ctx context.Context,
	request *core.AIRequest,
) (result *core.AIResult, err error) {
	started := time.Now()
	ctx, cancel := client.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := client.StartSpan(ctx, "ai.generate_response")
	defer func() { client.finishProviderSpan(ctx, span, "ai_request", started, result, err) }()
	span.SetAttribute("ai.provider", "gemini")
	span.SetAttribute("ai.provider_alias", "gemini")
	if request == nil {
		return nil, errors.New("gemini AI request is nil")
	}
	span.SetAttribute("ai.prompt_length", len(request.Prompt))

	invocation, err := client.prepareInvocation(ctx, request, false)
	if err != nil {
		prepared := preparedFromInvocation(invocation)
		client.recordRequestPreparation(ctx, span, prepared)
		return resultFromPrepared(prepared, nil, nil), err
	}
	prepared, route := invocation.Request, invocation.Route
	client.recordRequestPreparation(ctx, span, prepared)
	span.SetAttribute("ai.model", prepared.Model)
	span.SetAttribute("ai.surface", prepared.Report.Surface)
	span.SetAttribute("ai.request.route_identity", route.identity)
	if client.credentialSource == nil && strings.TrimSpace(client.apiKey) == "" {
		return resultFromPrepared(prepared, nil, nil), &integrationInvocationError{
			stage: "credential validation",
			cause: errors.New("gemini API key not configured"),
		}
	}

	client.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "gemini",
		ProviderAlias: "gemini",
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
		return resultFromPrepared(prepared, nil, nil), fmt.Errorf("create Gemini request: %w", err)
	}
	httpRequest.Header = prepared.Headers.Clone()
	credentialRequest := client.credentialRequest(prepared, route)
	response, err := client.executeWithCredential(ctx, httpRequest, credentialRequest)
	if err != nil {
		return resultFromPrepared(prepared, nil, nil), fmt.Errorf("send Gemini request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	client.observeCredentialRejection(ctx, credentialRequest, response.StatusCode)
	if response.StatusCode != http.StatusOK {
		body := readGeminiError(response)
		providerErr := client.HandleError(response.StatusCode, body, "Gemini", prepared.Model)
		span.SetAttribute("http.status_code", response.StatusCode)
		return resultFromPrepared(prepared, nil, nil), providerErr
	}

	decoded, err := prepared.Profile.decodeBuffered(response.Body, prepared.Model)
	if err != nil {
		return resultFromPrepared(prepared, nil, nil), err
	}
	client.LogResponseMetadata(ctx, providers.ResponseObservation{
		Provider:      "gemini",
		ProviderAlias: "gemini",
		SemanticModel: prepared.Model,
		Usage:         decoded.Response.Usage,
		Duration:      time.Since(started),
	})
	return resultFromPrepared(prepared, decoded.Response, decoded.UsageDetails), nil
}

func (client *Client) StreamResponse(
	ctx context.Context,
	prompt string,
	options *core.AIOptions,
	callback core.StreamCallback,
) (*core.AIResponse, error) {
	result, err := client.Stream(ctx, core.NewAIRequestFromLegacy(prompt, "", options), callback)
	if result != nil && result.Response != nil {
		return result.Response, err
	}
	return nil, err
}

func (client *Client) Stream(
	ctx context.Context,
	request *core.AIRequest,
	callback core.StreamCallback,
) (result *core.AIResult, err error) {
	started := time.Now()
	ctx, cancel := client.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := client.StartSpan(ctx, "ai.stream_response")
	defer func() { client.finishProviderSpan(ctx, span, "ai_stream", started, result, err) }()
	span.SetAttribute("ai.provider", "gemini")
	span.SetAttribute("ai.provider_alias", "gemini")
	span.SetAttribute("ai.streaming", true)
	if request == nil {
		return nil, errors.New("gemini AI request is nil")
	}
	if callback == nil {
		return nil, errors.New("gemini stream callback is nil")
	}
	span.SetAttribute("ai.prompt_length", len(request.Prompt))

	invocation, err := client.prepareInvocation(ctx, request, true)
	if err != nil {
		prepared := preparedFromInvocation(invocation)
		client.recordRequestPreparation(ctx, span, prepared)
		return resultFromPrepared(prepared, nil, nil), err
	}
	prepared, route := invocation.Request, invocation.Route
	client.recordRequestPreparation(ctx, span, prepared)
	span.SetAttribute("ai.model", prepared.Model)
	span.SetAttribute("ai.surface", prepared.Report.Surface)
	span.SetAttribute("ai.request.route_identity", route.identity)
	if client.credentialSource == nil && strings.TrimSpace(client.apiKey) == "" {
		return resultFromPrepared(prepared, nil, nil), &integrationInvocationError{
			stage: "credential validation",
			cause: errors.New("gemini API key not configured"),
		}
	}

	client.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "gemini",
		ProviderAlias: "gemini",
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
		return resultFromPrepared(prepared, nil, nil), fmt.Errorf("create Gemini stream request: %w", err)
	}
	httpRequest.Header = prepared.Headers.Clone()
	credentialRequest := client.credentialRequest(prepared, route)
	response, err := client.executeWithCredential(ctx, httpRequest, credentialRequest)
	if err != nil {
		return resultFromPrepared(prepared, nil, nil), fmt.Errorf("send Gemini stream request: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	client.observeCredentialRejection(ctx, credentialRequest, response.StatusCode)
	if response.StatusCode != http.StatusOK {
		body := readGeminiError(response)
		providerErr := client.HandleError(response.StatusCode, body, "Gemini", prepared.Model)
		span.SetAttribute("http.status_code", response.StatusCode)
		return resultFromPrepared(prepared, nil, nil), providerErr
	}

	decoder, err := prepared.Profile.newStreamDecoder(response.Body)
	if err != nil {
		return resultFromPrepared(prepared, nil, nil), err
	}
	var content strings.Builder
	var usage normalizedUsage
	var finishReason string
	chunkIndex := 0
	currentResult := func() *core.AIResult {
		tokenUsage, details := usage.coreUsage()
		return resultFromPrepared(prepared, &core.AIResponse{
			Content:  content.String(),
			Model:    prepared.Model,
			Provider: "gemini",
			Usage:    tokenUsage,
		}, details)
	}

	for {
		event, decodeErr := decoder.Next(ctx)
		if decodeErr != nil {
			if errors.Is(decodeErr, io.EOF) {
				break
			}
			if content.Len() > 0 {
				return currentResult(), errors.Join(core.ErrStreamPartiallyCompleted, decodeErr)
			}
			return resultFromPrepared(prepared, nil, nil), decodeErr
		}
		if event.Usage != nil {
			usage = *event.Usage
		}
		if event.FinishReason != "" {
			finishReason = event.FinishReason
		}
		if event.Text != "" {
			content.WriteString(event.Text)
			callbackErr := callback(core.StreamChunk{
				Content: event.Text,
				Delta:   true,
				Index:   chunkIndex,
				Model:   prepared.Model,
			})
			chunkIndex++
			if callbackErr != nil {
				span.SetAttribute("ai.stream_stopped_by_callback", true)
				span.SetAttribute("ai.stream_status", "callback_stop")
				return currentResult(), callbackErr
			}
		}
		if event.Done {
			break
		}
	}

	result = currentResult()
	finalErr := callback(core.StreamChunk{
		Delta:        false,
		Index:        chunkIndex,
		FinishReason: finishReason,
		Model:        prepared.Model,
		Usage:        &result.Response.Usage,
	})
	if finalErr != nil {
		span.SetAttribute("ai.stream_stopped_by_callback", true)
		span.SetAttribute("ai.stream_status", "callback_stop")
	}
	client.LogResponseMetadata(ctx, providers.ResponseObservation{
		Provider:      "gemini",
		ProviderAlias: "gemini",
		SemanticModel: prepared.Model,
		Usage:         result.Response.Usage,
		Duration:      time.Since(started),
	})
	return result, finalErr
}

func preparedFromInvocation(invocation *preparedInvocation) *preparedRequest {
	if invocation == nil {
		return nil
	}
	return invocation.Request
}

func resultFromPrepared(
	prepared *preparedRequest,
	response *core.AIResponse,
	usageDetails *core.AIUsageDetails,
) *core.AIResult {
	if prepared == nil && response == nil && usageDetails == nil {
		return nil
	}
	result := &core.AIResult{Response: response, UsageDetails: usageDetails}
	if prepared != nil {
		result.RequestReport = prepared.Report
	}
	return result
}

func (client *Client) finishProviderSpan(
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
		client.observeErrorWithDuration(ctx, span, operation, "provider_client", err, duration)
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

func (client *Client) SupportsStreaming() bool { return true }

var _ core.AIRequestClient = (*Client)(nil)
var _ core.StreamingAIRequestClient = (*Client)(nil)
var _ core.AIRequestFingerprinter = (*Client)(nil)
