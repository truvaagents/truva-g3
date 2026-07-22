package anthropic

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
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

const (
	// DefaultBaseURL is the default Anthropic API endpoint
	DefaultBaseURL = "https://api.anthropic.com/v1"
	// APIVersion is the required Anthropic API version header
	APIVersion = "2023-06-01"
)

// Client implements core.AIClient for Anthropic
type Client struct {
	*providers.BaseClient
	apiKey           string
	baseURL          string
	providerAlias    string
	defaultHeaders   map[string]string
	defaultExtra     map[string]interface{}
	requestPolicy    *requestpolicy.Engine
	credentialSource ai.CredentialSource
	endpointResolver ai.EndpointResolver
	requestTimeout   time.Duration
}

// NewClient creates a new Anthropic client with configuration
func NewClient(apiKey, baseURL string, logger core.Logger) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	base := providers.NewBaseClient(180*time.Second, logger) // 3 minutes default for reasoning models
	base.ProviderName = "anthropic"
	// Use "default" alias so resolveModel() is always called, enabling env var overrides
	// The actual model is resolved at request-time via modelAliases["default"]
	// or TRUVAG3_ANTHROPIC_MODEL_DEFAULT env var
	base.DefaultModel = "default"
	base.DefaultMaxTokens = 1000

	return &Client{
		BaseClient:     base,
		apiKey:         apiKey,
		baseURL:        baseURL,
		providerAlias:  "anthropic",
		requestPolicy:  newRequestPolicyEngine(),
		requestTimeout: 180 * time.Second,
	}
}

func (c *Client) observationAlias() string {
	if c.providerAlias == "" {
		return "anthropic"
	}
	return c.providerAlias
}

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
		Provider:      "anthropic",
		ProviderAlias: c.observationAlias(),
		ErrorType:     errorType,
		Duration:      duration,
	})
}

// GenerateResponse preserves the legacy AIClient surface by adapting it to the
// request-aware path.
func (c *Client) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
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

// Generate generates a response using Anthropic's native Messages API.
func (c *Client) Generate(ctx context.Context, request *core.AIRequest) (result *core.AIResult, err error) {
	started := time.Now()
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := c.StartSpan(ctx, "ai.generate_response")
	defer func() { c.finishProviderSpan(ctx, span, "ai_request", started, result, err) }()
	span.SetAttribute("ai.provider", "anthropic")
	span.SetAttribute("ai.provider_alias", c.observationAlias())
	if request == nil {
		return nil, errors.New("anthropic AI request is nil")
	}
	prompt := request.Prompt
	span.SetAttribute("ai.prompt_length", len(prompt))

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
	if c.credentialSource == nil && c.apiKey == "" {
		return resultWithReport(prepared, nil), errors.New("anthropic API key not configured")
	}

	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "anthropic",
		ProviderAlias: c.observationAlias(),
		SemanticModel: prepared.Model,
		PromptLength:  len(prompt),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, route.url.String(), bytes.NewReader(prepared.Body))
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("failed to create request: %w", err)
	}

	req.Header = prepared.Headers.Clone()

	credentialRequest := c.credentialRequest(prepared, route)
	resp, err := c.executeWithCredential(ctx, req, credentialRequest)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close() // Error can be safely ignored as we've read the body
	}()
	c.observeCredentialRejection(ctx, credentialRequest, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		apiErr := c.HandleError(resp.StatusCode, body, "Anthropic", prepared.Model)
		span.SetAttribute("http.status_code", resp.StatusCode)
		return resultWithReport(prepared, nil), apiErr
	}

	var anthropicResp AnthropicResponse
	if err := json.Unmarshal(body, &anthropicResp); err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text content from response
	var content string
	for _, item := range anthropicResp.Content {
		if item.Type == "text" {
			content += item.Text
		}
	}

	if content == "" {
		return resultWithReport(prepared, nil), errors.New("no text content in Anthropic response")
	}

	response := &core.AIResponse{
		Content:  content,
		Model:    anthropicResp.Model,
		Provider: "anthropic",
		Usage: core.TokenUsage{
			PromptTokens:     anthropicResp.Usage.InputTokens,
			CompletionTokens: anthropicResp.Usage.OutputTokens,
			TotalTokens:      anthropicResp.Usage.InputTokens + anthropicResp.Usage.OutputTokens,
		},
	}

	// Add token usage to span for cost tracking and debugging
	c.LogResponseMetadata(ctx, providers.ResponseObservation{
		Provider:      "anthropic",
		ProviderAlias: c.observationAlias(),
		SemanticModel: prepared.Model,
		Usage:         response.Usage,
		Duration:      time.Since(started),
	})

	return resultWithReport(prepared, response), nil
}

// StreamResponse preserves the legacy StreamingAIClient surface by adapting it
// to the request-aware path.
func (c *Client) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	result, err := c.Stream(ctx, core.NewAIRequestFromLegacy(prompt, "", options), callback)
	if result != nil && result.Response != nil {
		return result.Response, err
	}
	return nil, err
}

// Stream implements request-aware streaming for Anthropic's Messages API.
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
	span.SetAttribute("ai.provider", "anthropic")
	span.SetAttribute("ai.provider_alias", c.observationAlias())
	span.SetAttribute("ai.streaming", true)
	if request == nil {
		return nil, errors.New("anthropic AI request is nil")
	}
	if callback == nil {
		return nil, errors.New("anthropic stream callback is nil")
	}
	prompt := request.Prompt
	span.SetAttribute("ai.prompt_length", len(prompt))

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
	if c.credentialSource == nil && c.apiKey == "" {
		return resultWithReport(prepared, nil), errors.New("anthropic API key not configured")
	}

	// Log request
	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "anthropic",
		ProviderAlias: c.observationAlias(),
		SemanticModel: prepared.Model,
		PromptLength:  len(prompt),
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, route.url.String(), bytes.NewReader(prepared.Body))
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("failed to create request: %w", err)
	}

	req.Header = prepared.Headers.Clone()

	credentialRequest := c.credentialRequest(prepared, route)
	resp, err := c.executeWithCredential(ctx, req, credentialRequest)
	if err != nil {
		return resultWithReport(prepared, nil), fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	c.observeCredentialRejection(ctx, credentialRequest, resp.StatusCode)

	// Handle error responses
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		apiErr := c.HandleError(resp.StatusCode, body, "Anthropic", prepared.Model)
		span.SetAttribute("http.status_code", resp.StatusCode)
		return resultWithReport(prepared, nil), apiErr
	}

	// Parse SSE stream
	reader := bufio.NewReader(resp.Body)
	var fullContent strings.Builder
	var model string
	var inputTokens, outputTokens int
	chunkIndex := 0
	var finishReason string

	for {
		// Check context cancellation
		select {
		case <-ctx.Done():
			if fullContent.Len() > 0 {
				streamErr := core.ErrStreamPartiallyCompleted
				return resultWithReport(prepared, &core.AIResponse{
					Content:  fullContent.String(),
					Model:    model,
					Provider: "anthropic",
					Usage: core.TokenUsage{
						PromptTokens:     inputTokens,
						CompletionTokens: outputTokens,
						TotalTokens:      inputTokens + outputTokens,
					},
				}), streamErr
			}
			contextErr := ctx.Err()
			return resultWithReport(prepared, nil), contextErr
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			if fullContent.Len() > 0 {
				span.SetAttribute("ai.stream_partial", true)
				streamErr := core.ErrStreamPartiallyCompleted
				return resultWithReport(prepared, &core.AIResponse{
					Content:  fullContent.String(),
					Model:    model,
					Provider: "anthropic",
					Usage: core.TokenUsage{
						PromptTokens:     inputTokens,
						CompletionTokens: outputTokens,
						TotalTokens:      inputTokens + outputTokens,
					},
				}), streamErr
			}
			return resultWithReport(prepared, nil), fmt.Errorf("error reading stream: %w", err)
		}

		line = strings.TrimSpace(line)

		// Skip empty lines
		if line == "" {
			continue
		}

		// Parse event type
		if strings.HasPrefix(line, "event: ") {
			// Just continue to the data line
			continue
		}

		// Parse data line
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		var event StreamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			if c.Logger != nil {
				errorType, safeError := providers.SanitizedObservationError(err, "decode")
				fields := map[string]interface{}{
					"operation":  "ai_stream_parse",
					"provider":   "anthropic",
					"error":      safeError.Error(),
					"error_type": errorType,
				}
				providers.AddObservationRequestID(ctx, fields)
				c.Logger.DebugWithContext(ctx, "Anthropic stream - failed to parse event", fields)
			}
			continue
		}

		switch event.Type {
		case "message_start":
			if event.Message != nil {
				model = event.Message.Model
				if event.Message.Usage != nil {
					inputTokens = event.Message.Usage.InputTokens
				}
			}

		case "content_block_delta":
			if event.Delta != nil && event.Delta.Text != "" {
				fullContent.WriteString(event.Delta.Text)

				chunk := core.StreamChunk{
					Content: event.Delta.Text,
					Delta:   true,
					Index:   chunkIndex,
					Model:   model,
				}
				chunkIndex++

				if err := callback(chunk); err != nil {
					span.SetAttribute("ai.stream_stopped_by_callback", true)
					span.SetAttribute("ai.stream_status", "callback_stop")
					response := &core.AIResponse{
						Content:  fullContent.String(),
						Model:    model,
						Provider: "anthropic",
						Usage: core.TokenUsage{
							PromptTokens:     inputTokens,
							CompletionTokens: outputTokens,
							TotalTokens:      inputTokens + outputTokens,
						},
					}
					span.SetAttribute("ai.response_length", len(response.Content))
					span.SetAttribute("ai.chunks_sent", chunkIndex)
					c.LogResponseMetadata(ctx, providers.ResponseObservation{
						Provider:      "anthropic",
						ProviderAlias: c.observationAlias(),
						SemanticModel: prepared.Model,
						Usage:         response.Usage,
						Duration:      time.Since(started),
					})
					return resultWithReport(prepared, response), nil
				}
			}

		case "message_delta":
			if event.Delta != nil && event.Delta.StopReason != "" {
				finishReason = event.Delta.StopReason
			}
			if event.Usage != nil {
				outputTokens = event.Usage.OutputTokens
			}

		case "message_stop":
			// End of stream
		}
	}

	// Send final chunk with finish reason
	if finishReason != "" {
		usage := core.TokenUsage{
			PromptTokens:     inputTokens,
			CompletionTokens: outputTokens,
			TotalTokens:      inputTokens + outputTokens,
		}
		finalChunk := core.StreamChunk{
			Delta:        false,
			Index:        chunkIndex,
			FinishReason: finishReason,
			Model:        model,
			Usage:        &usage,
		}
		_ = callback(finalChunk)
	}

	response := &core.AIResponse{
		Content:  fullContent.String(),
		Model:    model,
		Provider: "anthropic",
		Usage: core.TokenUsage{
			PromptTokens:     inputTokens,
			CompletionTokens: outputTokens,
			TotalTokens:      inputTokens + outputTokens,
		},
	}

	// Add token usage to span
	span.SetAttribute("ai.prompt_tokens", response.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", response.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", response.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(response.Content))
	span.SetAttribute("ai.chunks_sent", chunkIndex)

	// Log response
	c.LogResponseMetadata(ctx, providers.ResponseObservation{
		Provider:      "anthropic",
		ProviderAlias: c.observationAlias(),
		SemanticModel: prepared.Model,
		Usage:         response.Usage,
		Duration:      time.Since(started),
	})

	return resultWithReport(prepared, response), nil
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
		c.observeError(ctx, span, operation, "provider_client", err, duration)
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

// SupportsStreaming returns true as Anthropic supports native streaming
func (c *Client) SupportsStreaming() bool {
	return true
}

func resultWithReport(prepared *preparedRequest, response *core.AIResponse) *core.AIResult {
	if response == nil && (prepared == nil || prepared.Report == nil) {
		return nil
	}
	result := &core.AIResult{Response: response}
	if prepared != nil {
		result.RequestReport = prepared.Report
	}
	return result
}

var _ core.AIRequestClient = (*Client)(nil)
var _ core.StreamingAIRequestClient = (*Client)(nil)
