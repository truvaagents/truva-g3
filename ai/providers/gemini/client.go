package gemini

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

const (
	// DefaultBaseURL is the default Gemini API endpoint
	DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"
)

// Client implements core.AIClient for Google Gemini
type Client struct {
	*providers.BaseClient
	apiKey         string
	baseURL        string
	defaultHeaders map[string]string
	defaultExtra   map[string]interface{}
}

// NewClient creates a new Gemini client with configuration
func NewClient(apiKey, baseURL string, logger core.Logger) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}

	base := providers.NewBaseClient(180*time.Second, logger) // 3 minutes default for reasoning models
	base.ProviderName = "gemini"
	base.DefaultModel = "default"
	base.DefaultMaxTokens = 1000

	return &Client{
		BaseClient: base,
		apiKey:     apiKey,
		baseURL:    baseURL,
	}
}

// GenerateResponse generates a response using Gemini's native GenerateContent API
func (c *Client) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	// Start distributed tracing span
	ctx, span := c.StartSpan(ctx, "ai.generate_response")
	defer span.End()

	// Set initial span attributes
	span.SetAttribute("ai.provider", "gemini")
	span.SetAttribute("ai.prompt_length", len(prompt))

	if c.apiKey == "" {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini request failed - API key not configured", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "gemini",
				"error":     "api_key_missing",
			})
		}
		span.RecordError(fmt.Errorf("API key not configured"))
		return nil, fmt.Errorf("gemini API key not configured")
	}

	// Apply defaults
	options = c.ApplyDefaults(options)

	// Resolve model alias (e.g., "smart" -> "gemini-1.5-pro")
	options.Model = resolveModel(options.Model)

	// Add model to span attributes after defaults are applied
	span.SetAttribute("ai.model", options.Model)

	// Log request
	c.LogRequest("gemini", options.Model, prompt)
	startTime := time.Now()

	// Build contents in Gemini format
	contents := []Content{
		{
			Role: "user",
			Parts: []Part{
				{Text: prompt},
			},
		},
	}

	// Build request body using native Gemini format
	reqBody := GeminiRequest{
		Contents: contents,
		GenerationConfig: &GenerationConfig{
			Temperature:     options.Temperature,
			MaxOutputTokens: options.MaxTokens,
		},
	}
	if options.ResponseFormat != "" {
		reqBody.GenerationConfig.ResponseMimeType = options.ResponseFormat
	}

	// Add system instruction if provided
	if options.SystemPrompt != "" {
		reqBody.SystemInstruction = &SystemInstruction{
			Parts: []Part{
				{Text: options.SystemPrompt},
			},
		}
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini request failed - marshal error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "gemini",
				"error":     err.Error(),
				"phase":     "request_preparation",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	if len(c.defaultExtra) > 0 || len(options.Extra) > 0 {
		reqBodyMap := map[string]interface{}{}
		if err := json.Unmarshal(jsonData, &reqBodyMap); err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to prepare gemini request body: %w", err)
		}
		for k, v := range providers.MergeAnyMaps(c.defaultExtra, options.Extra) {
			if _, exists := reqBodyMap[k]; exists {
				continue
			}
			reqBodyMap[k] = v
		}
		jsonData, err = json.Marshal(reqBodyMap)
		if err != nil {
			span.RecordError(err)
			return nil, fmt.Errorf("failed to marshal gemini request extras: %w", err)
		}
	}

	// Create HTTP request to native GenerateContent API endpoint
	// Format: /models/{model}:generateContent?key={api_key}
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", c.baseURL, options.Model, c.apiKey)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini request failed - create request error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "gemini",
				"error":     err.Error(),
				"phase":     "request_creation",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Content-Type", "application/json")
	providers.ApplyHeaders(req, map[string]struct{}{
		"content-type": {},
	}, c.defaultHeaders, options.Headers)

	// Execute with retry
	resp, err := c.ExecuteWithRetry(ctx, req)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini request failed - send error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "gemini",
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
			c.Logger.ErrorWithContext(ctx, "Gemini request failed - read response error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "gemini",
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
			c.Logger.ErrorWithContext(ctx, "Gemini request failed - API error", map[string]interface{}{
				"operation":   "ai_request_error",
				"provider":    "gemini",
				"status_code": resp.StatusCode,
				"phase":       "api_response",
			})
		}
		apiErr := c.HandleError(resp.StatusCode, body, "Gemini", options.Model)
		span.RecordError(apiErr)
		span.SetAttribute("http.status_code", resp.StatusCode)
		return nil, apiErr
	}

	// Parse response
	var geminiResp GeminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini request failed - parse response error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "gemini",
				"error":     err.Error(),
				"phase":     "response_parse",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text content from response
	if len(geminiResp.Candidates) == 0 {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini request failed - no candidates", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "gemini",
				"error":     "no_candidates_returned",
				"phase":     "response_validation",
			})
		}
		noCandidatesErr := fmt.Errorf("no candidates in Gemini response")
		span.RecordError(noCandidatesErr)
		return nil, noCandidatesErr
	}

	var content string
	candidate := geminiResp.Candidates[0]
	for _, part := range candidate.Content.Parts {
		content += part.Text
	}

	if content == "" {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini request failed - empty response", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "gemini",
				"error":     "no_text_content",
				"phase":     "response_validation",
			})
		}
		emptyErr := fmt.Errorf("no text content in Gemini response")
		span.RecordError(emptyErr)
		return nil, emptyErr
	}

	result := &core.AIResponse{
		Content:  content,
		Model:    options.Model,
		Provider: "gemini",
		Usage: core.TokenUsage{
			PromptTokens:     geminiResp.UsageMetadata.PromptTokenCount,
			CompletionTokens: geminiResp.UsageMetadata.CandidatesTokenCount,
			TotalTokens:      geminiResp.UsageMetadata.TotalTokenCount,
		},
	}

	// Add token usage to span for cost tracking and debugging
	span.SetAttribute("ai.prompt_tokens", result.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", result.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", result.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(result.Content))

	// Log response
	c.LogResponse(ctx, "gemini", result.Model, result.Usage, time.Since(startTime))
	c.LogResponseContent("gemini", result.Model, result.Content)

	return result, nil
}

// StreamResponse implements streaming for Gemini's streamGenerateContent API
func (c *Client) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	// Start distributed tracing span
	ctx, span := c.StartSpan(ctx, "ai.stream_response")
	defer span.End()

	// Set initial span attributes
	span.SetAttribute("ai.provider", "gemini")
	span.SetAttribute("ai.streaming", true)
	span.SetAttribute("ai.prompt_length", len(prompt))

	if c.apiKey == "" {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini streaming request failed - API key not configured", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "gemini",
				"error":     "api_key_missing",
			})
		}
		span.RecordError(fmt.Errorf("API key not configured"))
		return nil, fmt.Errorf("gemini API key not configured")
	}

	// Apply defaults
	options = c.ApplyDefaults(options)

	// Resolve model alias
	options.Model = resolveModel(options.Model)

	// Add model to span attributes
	span.SetAttribute("ai.model", options.Model)

	// Log request
	c.LogRequest("gemini", options.Model, prompt)
	startTime := time.Now()

	// Build request
	reqBody := GeminiRequest{
		Contents: []Content{
			{
				Role:  "user",
				Parts: []Part{{Text: prompt}},
			},
		},
		GenerationConfig: &GenerationConfig{
			Temperature:     options.Temperature,
			MaxOutputTokens: options.MaxTokens,
		},
	}

	// Add system instruction if provided
	if options.SystemPrompt != "" {
		reqBody.SystemInstruction = &SystemInstruction{
			Parts: []Part{{Text: options.SystemPrompt}},
		}
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini streaming request failed - marshal error", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "gemini",
				"error":     err.Error(),
				"phase":     "request_preparation",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build streaming URL
	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?key=%s&alt=sse", c.baseURL, options.Model, c.apiKey)

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini streaming request failed - create request error", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "gemini",
				"error":     err.Error(),
				"phase":     "request_creation",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	// Execute request
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini streaming request failed - send error", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "gemini",
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

	// Handle error responses
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Gemini streaming request failed - API error", map[string]interface{}{
				"operation":   "ai_stream_error",
				"provider":    "gemini",
				"status_code": resp.StatusCode,
				"phase":       "api_response",
			})
		}
		apiErr := c.HandleError(resp.StatusCode, body, "Gemini", options.Model)
		span.RecordError(apiErr)
		span.SetAttribute("http.status_code", resp.StatusCode)
		return nil, apiErr
	}

	// Parse SSE stream
	reader := bufio.NewReader(resp.Body)
	var fullContent strings.Builder
	var usage core.TokenUsage
	chunkIndex := 0
	var finishReason string

	for {
		// Check context cancellation
		select {
		case <-ctx.Done():
			if fullContent.Len() > 0 {
				return &core.AIResponse{
					Content:  fullContent.String(),
					Model:    options.Model,
					Provider: "gemini",
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
			if fullContent.Len() > 0 {
				span.SetAttribute("ai.stream_partial", true)
				return &core.AIResponse{
					Content:  fullContent.String(),
					Model:    options.Model,
					Provider: "gemini",
					Usage:    usage,
				}, core.ErrStreamPartiallyCompleted
			}
			span.RecordError(err)
			return nil, fmt.Errorf("error reading stream: %w", err)
		}

		line = strings.TrimSpace(line)

		// Skip empty lines
		if line == "" {
			continue
		}

		// Parse data line
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")

		var chunk StreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			if c.Logger != nil {
				c.Logger.DebugWithContext(ctx, "Gemini stream - failed to parse chunk", map[string]interface{}{
					"operation": "ai_stream_parse",
					"provider":  "gemini",
					"error":     err.Error(),
				})
			}
			continue
		}

		// Process candidates
		for _, candidate := range chunk.Candidates {
			// Extract text from parts
			for _, part := range candidate.Content.Parts {
				if part.Text != "" {
					fullContent.WriteString(part.Text)

					streamChunk := core.StreamChunk{
						Content: part.Text,
						Delta:   true,
						Index:   chunkIndex,
						Model:   options.Model,
					}
					chunkIndex++

					if err := callback(streamChunk); err != nil {
						span.SetAttribute("ai.stream_stopped_by_callback", true)
						return &core.AIResponse{
							Content:  fullContent.String(),
							Model:    options.Model,
							Provider: "gemini",
							Usage:    usage,
						}, nil
					}
				}
			}

			// Capture finish reason
			if candidate.FinishReason != "" {
				finishReason = candidate.FinishReason
			}
		}

		// Capture usage from chunk
		if chunk.UsageMetadata != nil {
			usage = core.TokenUsage{
				PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
				CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
			}
		}
	}

	// Send final chunk with finish reason
	if finishReason != "" {
		finalChunk := core.StreamChunk{
			Delta:        false,
			Index:        chunkIndex,
			FinishReason: finishReason,
			Model:        options.Model,
			Usage:        &usage,
		}
		_ = callback(finalChunk)
	}

	result := &core.AIResponse{
		Content:  fullContent.String(),
		Model:    options.Model,
		Provider: "gemini",
		Usage:    usage,
	}

	// Add token usage to span
	span.SetAttribute("ai.prompt_tokens", result.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", result.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", result.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(result.Content))
	span.SetAttribute("ai.chunks_sent", chunkIndex)

	// Log response
	c.LogResponse(ctx, "gemini", result.Model, result.Usage, time.Since(startTime))
	c.LogResponseContent("gemini", result.Model, result.Content)

	return result, nil
}

// SupportsStreaming returns true as Gemini supports native streaming
func (c *Client) SupportsStreaming() bool {
	return true
}
