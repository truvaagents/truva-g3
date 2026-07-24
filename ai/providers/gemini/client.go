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

func (c *Client) observeError(
	ctx context.Context,
	span core.Span,
	operation string,
	fallback string,
	err error,
) {
	errorType := providers.RecordObservationError(span, err, fallback)
	c.LogErrorMetadata(ctx, providers.ErrorObservation{
		Operation:     operation,
		Provider:      "gemini",
		ProviderAlias: "gemini",
		ErrorType:     errorType,
	})
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
		err := fmt.Errorf("gemini API key not configured")
		c.observeError(ctx, span, "ai_request", "credential", err)
		return nil, err
	}

	// Apply defaults
	options = c.ApplyDefaults(options)

	// Resolve model alias (e.g., "smart" -> "gemini-1.5-pro")
	options.Model = resolveModel(options.Model)

	// Add model to span attributes after defaults are applied
	span.SetAttribute("ai.model", options.Model)

	// Log request
	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "gemini",
		ProviderAlias: "gemini",
		SemanticModel: options.Model,
		PromptLength:  len(prompt),
	})
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
		c.observeError(ctx, span, "ai_request", "invalid_request", err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	if len(c.defaultExtra) > 0 || len(options.Extra) > 0 {
		reqBodyMap := map[string]interface{}{}
		if err := json.Unmarshal(jsonData, &reqBodyMap); err != nil {
			c.observeError(ctx, span, "ai_request", "invalid_request", err)
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
			c.observeError(ctx, span, "ai_request", "invalid_request", err)
			return nil, fmt.Errorf("failed to marshal gemini request extras: %w", err)
		}
	}

	// Create HTTP request to native GenerateContent API endpoint
	// Format: /models/{model}:generateContent?key={api_key}
	url := fmt.Sprintf("%s/models/%s:generateContent?key=%s", c.baseURL, options.Model, c.apiKey)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		c.observeError(ctx, span, "ai_request", "invalid_request", err)
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
		c.observeError(ctx, span, "ai_request", "transport", err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close() // Error can be safely ignored as we've read the body
	}()

	// Read response
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.observeError(ctx, span, "ai_request", "decode", err)
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Handle errors
	if resp.StatusCode != http.StatusOK {
		apiErr := c.HandleError(resp.StatusCode, body, "Gemini", options.Model)
		c.observeError(ctx, span, "ai_request", "provider_client", apiErr)
		span.SetAttribute("http.status_code", resp.StatusCode)
		return nil, apiErr
	}

	// Parse response
	var geminiResp GeminiResponse
	if err := json.Unmarshal(body, &geminiResp); err != nil {
		c.observeError(ctx, span, "ai_request", "decode", err)
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	// Extract text content from response
	if len(geminiResp.Candidates) == 0 {
		noCandidatesErr := fmt.Errorf("no candidates in Gemini response")
		c.observeError(ctx, span, "ai_request", "decode", noCandidatesErr)
		return nil, noCandidatesErr
	}

	var content string
	candidate := geminiResp.Candidates[0]
	for _, part := range candidate.Content.Parts {
		content += part.Text
	}

	if content == "" {
		emptyErr := fmt.Errorf("no text content in Gemini response")
		c.observeError(ctx, span, "ai_request", "decode", emptyErr)
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
	c.LogResponseMetadata(ctx, providers.ResponseObservation{
		Provider:      "gemini",
		ProviderAlias: "gemini",
		SemanticModel: options.Model,
		Usage:         result.Usage,
		Duration:      time.Since(startTime),
	})

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
		err := fmt.Errorf("gemini API key not configured")
		c.observeError(ctx, span, "ai_stream", "credential", err)
		return nil, err
	}

	// Apply defaults
	options = c.ApplyDefaults(options)

	// Resolve model alias
	options.Model = resolveModel(options.Model)

	// Add model to span attributes
	span.SetAttribute("ai.model", options.Model)

	// Log request metadata without prompt content.
	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "gemini",
		ProviderAlias: "gemini",
		SemanticModel: options.Model,
		PromptLength:  len(prompt),
	})
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
		c.observeError(ctx, span, "ai_stream", "invalid_request", err)
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Build streaming URL
	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?key=%s&alt=sse", c.baseURL, options.Model, c.apiKey)

	// Create HTTP request
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		c.observeError(ctx, span, "ai_stream", "invalid_request", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	// Execute request
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		c.observeError(ctx, span, "ai_stream", "transport", err)
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	// Handle error responses
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		apiErr := c.HandleError(resp.StatusCode, body, "Gemini", options.Model)
		c.observeError(ctx, span, "ai_stream", "provider_client", apiErr)
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
				streamErr := core.ErrStreamPartiallyCompleted
				c.observeError(ctx, span, "ai_stream", "partial_stream", streamErr)
				return &core.AIResponse{
					Content:  fullContent.String(),
					Model:    options.Model,
					Provider: "gemini",
					Usage:    usage,
				}, streamErr
			}
			ctxErr := ctx.Err()
			c.observeError(ctx, span, "ai_stream", "cancelled", ctxErr)
			return nil, ctxErr
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
				c.observeError(ctx, span, "ai_stream", "partial_stream", streamErr)
				return &core.AIResponse{
					Content:  fullContent.String(),
					Model:    options.Model,
					Provider: "gemini",
					Usage:    usage,
				}, streamErr
			}
			c.observeError(ctx, span, "ai_stream", "transport", err)
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
				errorType, safeError := providers.SanitizedObservationError(err, "decode")
				fields := map[string]interface{}{
					"operation":  "ai_stream_parse",
					"provider":   "gemini",
					"error":      safeError.Error(),
					"error_type": errorType,
				}
				providers.AddObservationRequestID(ctx, fields)
				c.Logger.DebugWithContext(ctx, "Gemini stream - failed to parse chunk", fields)
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
						span.SetAttribute("ai.stream_status", "callback_stop")
						response := &core.AIResponse{
							Content:  fullContent.String(),
							Model:    options.Model,
							Provider: "gemini",
							Usage:    usage,
						}
						span.SetAttribute("ai.response_length", len(response.Content))
						span.SetAttribute("ai.chunks_sent", chunkIndex)
						c.LogResponseMetadata(ctx, providers.ResponseObservation{
							Provider:      "gemini",
							ProviderAlias: "gemini",
							SemanticModel: options.Model,
							Usage:         response.Usage,
							Duration:      time.Since(startTime),
						})
						return response, nil
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

	// Log response metadata without response content.
	c.LogResponseMetadata(ctx, providers.ResponseObservation{
		Provider:      "gemini",
		ProviderAlias: "gemini",
		SemanticModel: options.Model,
		Usage:         result.Usage,
		Duration:      time.Since(startTime),
	})

	return result, nil
}

// SupportsStreaming returns true as Gemini supports native streaming
func (c *Client) SupportsStreaming() bool {
	return true
}
