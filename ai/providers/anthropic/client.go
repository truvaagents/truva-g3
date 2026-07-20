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

// GenerateResponse preserves the legacy AIClient surface by adapting it to the
// request-aware path.
func (c *Client) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	result, err := c.Generate(ctx, core.NewAIRequestFromLegacy(prompt, "", options))
	if result != nil && result.Response != nil {
		return result.Response, err
	}
	return nil, err
}

// Generate generates a response using Anthropic's native Messages API.
func (c *Client) Generate(ctx context.Context, request *core.AIRequest) (*core.AIResult, error) {
	if request == nil {
		return nil, errors.New("anthropic AI request is nil")
	}
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	prompt := request.Prompt
	// Start distributed tracing span
	ctx, span := c.StartSpan(ctx, "ai.generate_response")
	defer span.End()

	// Set initial span attributes
	span.SetAttribute("ai.provider", "anthropic")
	span.SetAttribute("ai.prompt_length", len(prompt))

	prepared, err := c.prepareAIRequest(ctx, request, false)
	if err != nil {
		if prepared != nil {
			c.recordRequestPreparation(ctx, span, prepared)
			span.SetAttribute("ai.model", prepared.Model)
		}
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic request failed - preparation error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "anthropic",
				"error":     err.Error(),
				"phase":     "request_preparation",
			})
		}
		span.RecordError(err)
		return resultWithReport(prepared, nil), err
	}
	route, err := c.resolveEndpoint(ctx, prepared)
	if err != nil {
		c.recordRequestPreparation(ctx, span, prepared)
		span.SetAttribute("ai.model", prepared.Model)
		span.RecordError(err)
		return resultWithReport(prepared, nil), err
	}
	c.bindRoute(prepared, route)
	c.recordRequestPreparation(ctx, span, prepared)

	// Add sanitized route and model identities after semantic preparation.
	span.SetAttribute("ai.model", prepared.Model)
	span.SetAttribute("ai.request.route_identity", route.identity)
	if c.credentialSource == nil && c.apiKey == "" {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic request failed - API key not configured", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "anthropic",
				"error":     "api_key_missing",
			})
		}
		missingKey := errors.New("anthropic API key not configured")
		span.RecordError(missingKey)
		return resultWithReport(prepared, nil), missingKey
	}

	// Log request
	c.LogRequest("anthropic", prepared.Model, prompt)
	startTime := time.Now()

	// Create HTTP request to native Messages API endpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, route.url.String(), bytes.NewReader(prepared.Body))
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic request failed - create request error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "anthropic",
				"error":     err.Error(),
				"phase":     "request_creation",
			})
		}
		span.RecordError(err)
		return resultWithReport(prepared, nil), fmt.Errorf("failed to create request: %w", err)
	}

	req.Header = prepared.Headers.Clone()

	credentialRequest := c.credentialRequest(prepared, route)
	// Execute with retry. Dynamic credentials are acquired after semantic
	// preparation for each fresh transport attempt.
	resp, err := c.executeWithCredential(ctx, req, credentialRequest)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic request failed - send error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "anthropic",
				"error":     err.Error(),
				"phase":     "request_execution",
			})
		}
		span.RecordError(err)
		return resultWithReport(prepared, nil), fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close() // Error can be safely ignored as we've read the body
	}()
	c.observeCredentialRejection(ctx, credentialRequest, resp.StatusCode)

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic request failed - read response error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "anthropic",
				"error":     err.Error(),
				"phase":     "response_read",
			})
		}
		span.RecordError(err)
		return resultWithReport(prepared, nil), fmt.Errorf("failed to read response: %w", err)
	}

	// Handle errors
	if resp.StatusCode != http.StatusOK {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic request failed - API error", map[string]interface{}{
				"operation":   "ai_request_error",
				"provider":    "anthropic",
				"status_code": resp.StatusCode,
				"phase":       "api_response",
			})
		}
		apiErr := c.HandleError(resp.StatusCode, body, "Anthropic", prepared.Model)
		span.RecordError(apiErr)
		span.SetAttribute("http.status_code", resp.StatusCode)
		return resultWithReport(prepared, nil), apiErr
	}

	// Parse response
	var anthropicResp AnthropicResponse
	if err := json.Unmarshal(body, &anthropicResp); err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic request failed - parse response error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "anthropic",
				"error":     err.Error(),
				"phase":     "response_parse",
			})
		}
		span.RecordError(err)
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
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic request failed - empty response", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "anthropic",
				"error":     "no_text_content",
				"phase":     "response_validation",
			})
		}
		emptyErr := fmt.Errorf("no text content in Anthropic response")
		span.RecordError(emptyErr)
		return resultWithReport(prepared, nil), emptyErr
	}

	result := &core.AIResponse{
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
	span.SetAttribute("ai.prompt_tokens", result.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", result.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", result.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(result.Content))

	// Log response
	c.LogResponse(ctx, "anthropic", result.Model, result.Usage, time.Since(startTime))
	c.LogResponseContent("anthropic", result.Model, result.Content)

	return resultWithReport(prepared, result), nil
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
func (c *Client) Stream(ctx context.Context, request *core.AIRequest, callback core.StreamCallback) (*core.AIResult, error) {
	if request == nil {
		return nil, errors.New("anthropic AI request is nil")
	}
	if callback == nil {
		return nil, errors.New("anthropic stream callback is nil")
	}
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	prompt := request.Prompt
	// Start distributed tracing span
	ctx, span := c.StartSpan(ctx, "ai.stream_response")
	defer span.End()

	// Set initial span attributes
	span.SetAttribute("ai.provider", "anthropic")
	span.SetAttribute("ai.streaming", true)
	span.SetAttribute("ai.prompt_length", len(prompt))

	prepared, err := c.prepareAIRequest(ctx, request, true)
	if err != nil {
		if prepared != nil {
			c.recordRequestPreparation(ctx, span, prepared)
			span.SetAttribute("ai.model", prepared.Model)
		}
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic streaming request failed - preparation error", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "anthropic",
				"error":     err.Error(),
				"phase":     "request_preparation",
			})
		}
		span.RecordError(err)
		return resultWithReport(prepared, nil), err
	}
	route, err := c.resolveEndpoint(ctx, prepared)
	if err != nil {
		c.recordRequestPreparation(ctx, span, prepared)
		span.SetAttribute("ai.model", prepared.Model)
		span.RecordError(err)
		return resultWithReport(prepared, nil), err
	}
	c.bindRoute(prepared, route)
	c.recordRequestPreparation(ctx, span, prepared)

	// Add sanitized route and model identities after semantic preparation.
	span.SetAttribute("ai.model", prepared.Model)
	span.SetAttribute("ai.request.route_identity", route.identity)
	if c.credentialSource == nil && c.apiKey == "" {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic streaming request failed - API key not configured", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "anthropic",
				"error":     "api_key_missing",
			})
		}
		missingKey := errors.New("anthropic API key not configured")
		span.RecordError(missingKey)
		return resultWithReport(prepared, nil), missingKey
	}

	// Log request
	c.LogRequest("anthropic", prepared.Model, prompt)
	startTime := time.Now()

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, route.url.String(), bytes.NewReader(prepared.Body))
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic streaming request failed - create request error", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "anthropic",
				"error":     err.Error(),
				"phase":     "request_creation",
			})
		}
		span.RecordError(err)
		return resultWithReport(prepared, nil), fmt.Errorf("failed to create request: %w", err)
	}

	req.Header = prepared.Headers.Clone()

	credentialRequest := c.credentialRequest(prepared, route)
	if err := c.prepareCredential(ctx, req, credentialRequest); err != nil {
		span.RecordError(err)
		return resultWithReport(prepared, nil), err
	}

	// Execute request
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		err = &integrationInvocationError{stage: "transport request", cause: err}
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic streaming request failed - send error", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "anthropic",
				"error":     err.Error(),
				"phase":     "request_execution",
			})
		}
		span.RecordError(err)
		return resultWithReport(prepared, nil), fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	c.observeCredentialRejection(ctx, credentialRequest, resp.StatusCode)

	// Handle error responses
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Anthropic streaming request failed - API error", map[string]interface{}{
				"operation":   "ai_stream_error",
				"provider":    "anthropic",
				"status_code": resp.StatusCode,
				"phase":       "api_response",
			})
		}
		apiErr := c.HandleError(resp.StatusCode, body, "Anthropic", prepared.Model)
		span.RecordError(apiErr)
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
				return resultWithReport(prepared, &core.AIResponse{
					Content:  fullContent.String(),
					Model:    model,
					Provider: "anthropic",
					Usage: core.TokenUsage{
						PromptTokens:     inputTokens,
						CompletionTokens: outputTokens,
						TotalTokens:      inputTokens + outputTokens,
					},
				}), core.ErrStreamPartiallyCompleted
			}
			return resultWithReport(prepared, nil), ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			if fullContent.Len() > 0 {
				span.SetAttribute("ai.stream_partial", true)
				return resultWithReport(prepared, &core.AIResponse{
					Content:  fullContent.String(),
					Model:    model,
					Provider: "anthropic",
					Usage: core.TokenUsage{
						PromptTokens:     inputTokens,
						CompletionTokens: outputTokens,
						TotalTokens:      inputTokens + outputTokens,
					},
				}), core.ErrStreamPartiallyCompleted
			}
			span.RecordError(err)
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
				c.Logger.DebugWithContext(ctx, "Anthropic stream - failed to parse event", map[string]interface{}{
					"operation": "ai_stream_parse",
					"provider":  "anthropic",
					"error":     err.Error(),
				})
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
					return resultWithReport(prepared, &core.AIResponse{
						Content:  fullContent.String(),
						Model:    model,
						Provider: "anthropic",
						Usage: core.TokenUsage{
							PromptTokens:     inputTokens,
							CompletionTokens: outputTokens,
							TotalTokens:      inputTokens + outputTokens,
						},
					}), nil
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

	result := &core.AIResponse{
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
	span.SetAttribute("ai.prompt_tokens", result.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", result.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", result.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(result.Content))
	span.SetAttribute("ai.chunks_sent", chunkIndex)

	// Log response
	c.LogResponse(ctx, "anthropic", result.Model, result.Usage, time.Since(startTime))
	c.LogResponseContent("anthropic", result.Model, result.Content)

	return resultWithReport(prepared, result), nil
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
