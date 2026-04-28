package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

// Client implements core.AIClient for OpenAI
type Client struct {
	*providers.BaseClient
	apiKey                   string
	baseURL                  string
	providerAlias            string // For request-time alias resolution (e.g., "openai.deepseek")
	defaultHeaders           map[string]string
	defaultExtra             map[string]interface{}
	ReasoningTokenMultiplier int    // Token multiplier for reasoning models (0 = use default 5x)
	ReasoningEffort          string // Default reasoning effort: "none", "low", "medium", "high", "xhigh" (empty = model default)
}

// NewClient creates a new OpenAI client with configuration
func NewClient(apiKey, baseURL, providerAlias string, logger core.Logger) *Client {
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	base := providers.NewBaseClient(180*time.Second, logger) // 3 minutes default for reasoning models
	base.ProviderName = "openai"
	// Use "default" alias so ResolveModel() is always called, enabling env var overrides
	// The actual model is resolved at request-time via ModelAliases["openai"]["default"]
	// or TRUVAG3_OPENAI_MODEL_DEFAULT env var
	base.DefaultModel = "default"

	return &Client{
		BaseClient:    base,
		apiKey:        apiKey,
		baseURL:       baseURL,
		providerAlias: providerAlias,
	}
}

// getProviderName returns the provider name for AIResponse.
// Falls back to "openai" if providerAlias is not set.
func (c *Client) getProviderName() string {
	if c.providerAlias == "" {
		return "openai"
	}
	return c.providerAlias
}

// truncateForLog truncates a string for logging purposes
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func (c *Client) requiresAPIKey() bool {
	return c.getProviderName() != "openai.ollama"
}

func applyAliasSpecificReasoning(providerAlias string, reqBody map[string]interface{}, reasoningEffort string) {
	if reasoningEffort == "" {
		return
	}
	if _, exists := reqBody["reasoning"]; exists {
		return
	}

	// Ollama's OpenAI-compatible endpoint accepts reasoning controls even for
	// non-OpenAI-native model names like gemma/qwen/deepseek tags hosted locally.
	if providerAlias == "openai.ollama" {
		reqBody["reasoning"] = map[string]interface{}{
			"effort": reasoningEffort,
		}
	}
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
	for k, v := range extra {
		switch strings.ToLower(k) {
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
		filtered[k] = v
	}

	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

// GenerateResponse generates a response using OpenAI
func (c *Client) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	// Start distributed tracing span
	ctx, span := c.StartSpan(ctx, "ai.generate_response")
	defer span.End()

	// Set initial span attributes
	span.SetAttribute("ai.provider", "openai")
	span.SetAttribute("ai.prompt_length", len(prompt))

	if c.requiresAPIKey() && c.apiKey == "" {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI request failed - API key not configured", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  c.getProviderName(),
				"error":     "api_key_missing",
			})
		}
		span.RecordError(fmt.Errorf("API key not configured"))
		return nil, fmt.Errorf("OpenAI API key not configured")
	}

	// Apply defaults
	options = c.ApplyDefaults(options)

	// Resolve model alias at request time (e.g., "smart" -> "gpt-4")
	options.Model = ResolveModel(c.providerAlias, options.Model)

	// Add model to span attributes after defaults are applied
	span.SetAttribute("ai.model", options.Model)

	// Log request
	c.LogRequest("openai", options.Model, prompt)
	startTime := time.Now()

	// Build messages
	messages := []map[string]string{}

	if options.SystemPrompt != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": options.SystemPrompt,
		})
	}

	messages = append(messages, map[string]string{
		"role":    "user",
		"content": prompt,
	})

	// Resolve reasoning effort: per-request overrides client default
	providerAlias := c.getProviderName()
	caps := providers.LookupModelCapabilities(providerAlias, options.Model)

	reasoningEffort := options.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = c.ReasoningEffort
	}
	if reasoningEffort != "" && caps.ReasoningStyle != "openai" {
		providers.LogTranslationDegraded(ctx, c.Logger, providerAlias, options.Model, "reasoning_effort_stripped", "reasoning_effort")
		reasoningEffort = ""
	}

	responseFormat := options.ResponseFormat
	if responseFormat != "" && !caps.SupportsJSONMode {
		providers.LogTranslationDegraded(ctx, c.Logger, providerAlias, options.Model, "response_format_stripped", "response_format")
		responseFormat = ""
	}
	filteredExtra := filterOpenAIExtraFields(ctx, c.Logger, providerAlias, options.Model, caps, providers.MergeAnyMaps(c.defaultExtra, options.Extra))

	// Build request body (handles reasoning model differences automatically)
	reqBody := buildRequestBody(options.Model, messages, options.MaxTokens, options.Temperature, false, c.ReasoningTokenMultiplier, reasoningEffort)
	applyAliasSpecificReasoning(providerAlias, reqBody, reasoningEffort)
	if responseFormat != "" {
		reqBody["response_format"] = map[string]interface{}{"type": responseFormat}
	}
	for k, v := range filteredExtra {
		if _, exists := reqBody[k]; exists {
			continue
		}
		reqBody[k] = v
	}

	// Log reasoning model parameter adjustments (uses WithContext for trace correlation)
	if c.Logger != nil && IsReasoningModel(options.Model) {
		effectiveMultiplier := c.ReasoningTokenMultiplier
		if effectiveMultiplier <= 0 {
			effectiveMultiplier = DefaultReasoningTokenMultiplier
		}
		if reasoningEffort == "none" {
			effectiveMultiplier = 1 // No multiplier applied for effort=none
		}
		c.Logger.DebugWithContext(ctx, "Using reasoning model parameters", map[string]interface{}{
			"operation":                   "ai_request_params",
			"provider":                    "openai",
			"model":                       options.Model,
			"using_max_completion_tokens": true,
			"temperature_omitted":         reasoningEffort != "none",
			"effective_token_multiplier":  effectiveMultiplier,
			"reasoning_effort":            reasoningEffort,
		})
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI request failed - marshal error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "openai",
				"error":     err.Error(),
				"phase":     "request_preparation",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI request failed - create request error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "openai",
				"error":     err.Error(),
				"phase":     "request_creation",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	providers.ApplyHeaders(req, map[string]struct{}{
		"authorization": {},
		"content-type":  {},
	}, c.defaultHeaders, options.Headers)

	// Execute with retry
	resp, err := c.ExecuteWithRetry(ctx, req)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI request failed - send error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "openai",
				"error":     err.Error(),
				"phase":     "request_execution",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close() // Error can be safely ignored as we've read the body
	}()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI request failed - read response error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "openai",
				"error":     err.Error(),
				"phase":     "response_read",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Handle errors
	if resp.StatusCode != http.StatusOK {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI request failed - API error", map[string]interface{}{
				"operation":   "ai_request_error",
				"provider":    "openai",
				"status_code": resp.StatusCode,
				"phase":       "api_response",
			})
		}
		apiErr := c.HandleError(resp.StatusCode, body, "OpenAI", options.Model)
		span.RecordError(apiErr)
		span.SetAttribute("http.status_code", resp.StatusCode)
		return nil, apiErr
	}

	// Parse response
	var openAIResp OpenAIResponse
	if err := json.Unmarshal(body, &openAIResp); err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI request failed - parse response error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "openai",
				"error":     err.Error(),
				"phase":     "response_parse",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Debug: Log raw response for reasoning model investigation (uses WithContext for trace correlation)
	if c.Logger != nil && IsReasoningModel(options.Model) {
		// Truncate raw body for logging (first 2000 chars)
		rawBodyStr := string(body)
		if len(rawBodyStr) > 2000 {
			rawBodyStr = rawBodyStr[:2000] + "...[truncated]"
		}
		c.Logger.DebugWithContext(ctx, "Raw OpenAI response for reasoning model", map[string]interface{}{
			"operation":         "ai_raw_response_debug",
			"provider":          "openai",
			"model":             options.Model,
			"raw_response":      rawBodyStr,
			"choices_count":     len(openAIResp.Choices),
			"completion_tokens": openAIResp.Usage.CompletionTokens,
		})
	}

	if len(openAIResp.Choices) == 0 {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI request failed - empty response", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "openai",
				"error":     "no_choices_returned",
				"phase":     "response_validation",
			})
		}
		emptyErr := fmt.Errorf("no response from OpenAI")
		span.RecordError(emptyErr)
		return nil, emptyErr
	}

	// Debug: Log parsed message fields for reasoning model investigation (uses WithContext for trace correlation)
	if c.Logger != nil && IsReasoningModel(options.Model) {
		msg := openAIResp.Choices[0].Message
		c.Logger.DebugWithContext(ctx, "Parsed message fields for reasoning model", map[string]interface{}{
			"operation":                 "ai_parsed_message_debug",
			"provider":                  "openai",
			"model":                     options.Model,
			"content_length":            len(msg.Content),
			"reasoning_content_length":  len(msg.ReasoningContent),
			"content_preview":           truncateForLog(msg.Content, 200),
			"reasoning_content_preview": truncateForLog(msg.ReasoningContent, 200),
			"role":                      msg.Role,
		})
	}

	// Extract content - for reasoning models (GPT-5, o1, o3, o4), content may be in ReasoningContent
	responseContent := openAIResp.Choices[0].Message.Content
	if responseContent == "" && openAIResp.Choices[0].Message.ReasoningContent != "" {
		responseContent = openAIResp.Choices[0].Message.ReasoningContent
	}

	result := &core.AIResponse{
		Content:  responseContent,
		Model:    openAIResp.Model,
		Provider: c.getProviderName(),
		Usage: core.TokenUsage{
			PromptTokens:     openAIResp.Usage.PromptTokens,
			CompletionTokens: openAIResp.Usage.CompletionTokens,
			TotalTokens:      openAIResp.Usage.TotalTokens,
		},
	}

	// Add token usage to span for cost tracking and debugging
	span.SetAttribute("ai.prompt_tokens", result.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", result.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", result.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(result.Content))
	if options.Temperature > 0 {
		span.SetAttribute("ai.temperature", float64(options.Temperature))
	}
	if options.MaxTokens > 0 {
		span.SetAttribute("ai.max_tokens", options.MaxTokens)
	}
	if openAIResp.Choices[0].FinishReason != "" {
		span.SetAttribute("ai.finish_reason", openAIResp.Choices[0].FinishReason)
	}
	span.SetAttribute("ai.duration_ms", time.Since(startTime).Milliseconds())
	if cost, known := providers.EstimateCostUSD(result.Model, result.Usage.PromptTokens, result.Usage.CompletionTokens); known {
		span.SetAttribute("ai.cost_usd", cost)
	}

	// Log response
	c.LogResponse(ctx, "openai", result.Model, result.Usage, time.Since(startTime))
	c.LogResponseContent("openai", result.Model, result.Content)

	return result, nil
}

// StreamResponse implements streaming for OpenAI API using Server-Sent Events
func (c *Client) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	// Start distributed tracing span
	ctx, span := c.StartSpan(ctx, "ai.stream_response")
	defer span.End()

	// Set initial span attributes
	span.SetAttribute("ai.provider", "openai")
	span.SetAttribute("ai.streaming", true)
	span.SetAttribute("ai.prompt_length", len(prompt))

	if c.requiresAPIKey() && c.apiKey == "" {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI streaming request failed - API key not configured", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  c.getProviderName(),
				"error":     "api_key_missing",
			})
		}
		span.RecordError(fmt.Errorf("API key not configured"))
		return nil, fmt.Errorf("OpenAI API key not configured")
	}

	// Apply defaults
	options = c.ApplyDefaults(options)

	// Resolve model alias at request time
	options.Model = ResolveModel(c.providerAlias, options.Model)

	// Add model to span attributes after defaults are applied
	span.SetAttribute("ai.model", options.Model)

	// Log request
	c.LogRequest("openai", options.Model, prompt)
	startTime := time.Now()

	// Build messages
	messages := []map[string]string{}

	if options.SystemPrompt != "" {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": options.SystemPrompt,
		})
	}

	messages = append(messages, map[string]string{
		"role":    "user",
		"content": prompt,
	})

	// Resolve reasoning effort: per-request overrides client default
	providerAlias := c.getProviderName()
	caps := providers.LookupModelCapabilities(providerAlias, options.Model)

	reasoningEffort := options.ReasoningEffort
	if reasoningEffort == "" {
		reasoningEffort = c.ReasoningEffort
	}
	if reasoningEffort != "" && caps.ReasoningStyle != "openai" {
		providers.LogTranslationDegraded(ctx, c.Logger, providerAlias, options.Model, "reasoning_effort_stripped", "reasoning_effort")
		reasoningEffort = ""
	}

	responseFormat := options.ResponseFormat
	if responseFormat != "" && !caps.SupportsJSONMode {
		providers.LogTranslationDegraded(ctx, c.Logger, providerAlias, options.Model, "response_format_stripped", "response_format")
		responseFormat = ""
	}
	filteredExtra := filterOpenAIExtraFields(ctx, c.Logger, providerAlias, options.Model, caps, providers.MergeAnyMaps(c.defaultExtra, options.Extra))

	// Build request body with streaming enabled (handles reasoning model differences automatically)
	reqBody := buildRequestBody(options.Model, messages, options.MaxTokens, options.Temperature, true, c.ReasoningTokenMultiplier, reasoningEffort)
	applyAliasSpecificReasoning(providerAlias, reqBody, reasoningEffort)
	if responseFormat != "" {
		reqBody["response_format"] = map[string]interface{}{"type": responseFormat}
	}
	for k, v := range filteredExtra {
		if _, exists := reqBody[k]; exists {
			continue
		}
		reqBody[k] = v
	}

	// Log reasoning model parameter adjustments (uses WithContext for trace correlation)
	if c.Logger != nil && IsReasoningModel(options.Model) {
		effectiveMultiplier := c.ReasoningTokenMultiplier
		if effectiveMultiplier <= 0 {
			effectiveMultiplier = DefaultReasoningTokenMultiplier
		}
		if reasoningEffort == "none" {
			effectiveMultiplier = 1 // No multiplier applied for effort=none
		}
		c.Logger.DebugWithContext(ctx, "Using reasoning model parameters for streaming", map[string]interface{}{
			"operation":                   "ai_stream_params",
			"provider":                    "openai",
			"model":                       options.Model,
			"using_max_completion_tokens": true,
			"temperature_omitted":         reasoningEffort != "none",
			"effective_token_multiplier":  effectiveMultiplier,
			"reasoning_effort":            reasoningEffort,
		})
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI streaming request failed - marshal error", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "openai",
				"error":     err.Error(),
				"phase":     "request_preparation",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI streaming request failed - create request error", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "openai",
				"error":     err.Error(),
				"phase":     "request_creation",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	providers.ApplyHeaders(req, map[string]struct{}{
		"authorization": {},
		"content-type":  {},
	}, c.defaultHeaders, options.Headers)
	req.Header.Set("Accept", "text/event-stream")

	// Execute request (no retry for streaming - connection establishment only)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI streaming request failed - send error", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "openai",
				"error":     err.Error(),
				"phase":     "request_execution",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Handle non-streaming error responses
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "OpenAI streaming request failed - API error", map[string]interface{}{
				"operation":   "ai_stream_error",
				"provider":    "openai",
				"status_code": resp.StatusCode,
				"phase":       "api_response",
			})
		}
		apiErr := c.HandleError(resp.StatusCode, body, "OpenAI", options.Model)
		span.RecordError(apiErr)
		span.SetAttribute("http.status_code", resp.StatusCode)
		return nil, apiErr
	}

	// Parse SSE stream
	reader := bufio.NewReader(resp.Body)
	var fullContent strings.Builder
	var model string
	var usage core.TokenUsage
	chunkIndex := 0
	var finishReason string

	for {
		// Check context cancellation
		select {
		case <-ctx.Done():
			// Return partial result with what we have
			if fullContent.Len() > 0 {
				return &core.AIResponse{
					Content:  fullContent.String(),
					Model:    model,
					Provider: c.getProviderName(),
					Usage:    usage,
				}, core.ErrStreamPartiallyCompleted
			}
			return nil, ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			// Partial completion - return what we have
			if fullContent.Len() > 0 {
				span.SetAttribute("ai.stream_partial", true)
				return &core.AIResponse{
					Content:  fullContent.String(),
					Model:    model,
					Provider: c.getProviderName(),
					Usage:    usage,
				}, core.ErrStreamPartiallyCompleted
			}
			span.RecordError(err)
			return nil, fmt.Errorf("error reading stream: %w", err)
		}

		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		// Check for stream end
		if line == "data: [DONE]" {
			break
		}

		// Parse SSE data line
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		var streamResp StreamResponse
		if err := json.Unmarshal([]byte(data), &streamResp); err != nil {
			// Log but continue - some chunks may be malformed
			if c.Logger != nil {
				c.Logger.DebugWithContext(ctx, "OpenAI stream - failed to parse chunk", map[string]interface{}{
					"operation": "ai_stream_parse",
					"provider":  "openai",
					"error":     err.Error(),
				})
			}
			continue
		}

		// Capture model from first chunk
		if model == "" && streamResp.Model != "" {
			model = streamResp.Model
		}

		// Capture usage from final chunk (if stream_options.include_usage was set)
		if streamResp.Usage != nil {
			usage = core.TokenUsage{
				PromptTokens:     streamResp.Usage.PromptTokens,
				CompletionTokens: streamResp.Usage.CompletionTokens,
				TotalTokens:      streamResp.Usage.TotalTokens,
			}
		}

		// Process choices
		for _, choice := range streamResp.Choices {
			// Extract content - for reasoning models (GPT-5, o1, o3, o4), content may be in ReasoningContent
			deltaContent := choice.Delta.Content
			if deltaContent == "" && choice.Delta.ReasoningContent != "" {
				deltaContent = choice.Delta.ReasoningContent
			}

			if deltaContent != "" {
				fullContent.WriteString(deltaContent)

				// Create chunk and call callback
				chunk := core.StreamChunk{
					Content: deltaContent,
					Delta:   true,
					Index:   chunkIndex,
					Model:   model,
				}
				chunkIndex++

				if err := callback(chunk); err != nil {
					// Callback requested stop
					span.SetAttribute("ai.stream_stopped_by_callback", true)
					return &core.AIResponse{
						Content:  fullContent.String(),
						Model:    model,
						Provider: c.getProviderName(),
						Usage:    usage,
					}, nil
				}
			}

			// Capture finish reason
			if choice.FinishReason != "" {
				finishReason = choice.FinishReason
			}
		}
	}

	// Send final chunk with finish reason
	if finishReason != "" {
		finalChunk := core.StreamChunk{
			Delta:        false,
			Index:        chunkIndex,
			FinishReason: finishReason,
			Model:        model,
			Usage:        &usage,
		}
		_ = callback(finalChunk) // Ignore error on final chunk
	}

	result := &core.AIResponse{
		Content:  fullContent.String(),
		Model:    model,
		Provider: c.getProviderName(),
		Usage:    usage,
	}

	// Add token usage to span for cost tracking
	span.SetAttribute("ai.prompt_tokens", result.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", result.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", result.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(result.Content))
	span.SetAttribute("ai.chunks_sent", chunkIndex)
	if options.Temperature > 0 {
		span.SetAttribute("ai.temperature", float64(options.Temperature))
	}
	if options.MaxTokens > 0 {
		span.SetAttribute("ai.max_tokens", options.MaxTokens)
	}
	if finishReason != "" {
		span.SetAttribute("ai.finish_reason", finishReason)
	}
	span.SetAttribute("ai.duration_ms", time.Since(startTime).Milliseconds())
	if cost, known := providers.EstimateCostUSD(result.Model, result.Usage.PromptTokens, result.Usage.CompletionTokens); known {
		span.SetAttribute("ai.cost_usd", cost)
	}

	// Log response
	c.LogResponse(ctx, "openai", result.Model, result.Usage, time.Since(startTime))
	c.LogResponseContent("openai", result.Model, result.Content)

	return result, nil
}

// SupportsStreaming returns true as OpenAI supports native streaming
func (c *Client) SupportsStreaming() bool {
	return true
}
