//go:build bedrock
// +build bedrock

package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
)

// Client implements core.AIClient for AWS Bedrock
type Client struct {
	*providers.BaseClient
	bedrockClient *bedrockruntime.Client
	region        string
}

// NewClient creates a new AWS Bedrock client
func NewClient(cfg aws.Config, region string, logger core.Logger) *Client {
	// Create Bedrock Runtime client
	bedrockClient := bedrockruntime.NewFromConfig(cfg)

	// Create base client with defaults
	base := providers.NewBaseClient(180*time.Second, logger) // 3 minutes default for reasoning models
	base.DefaultModel = ModelClaude3Sonnet                   // Default to Claude Sonnet
	base.DefaultMaxTokens = 1000

	return &Client{
		BaseClient:    base,
		bedrockClient: bedrockClient,
		region:        region,
	}
}

// GenerateResponse generates a response using AWS Bedrock's Converse API
func (c *Client) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	// Start distributed tracing span
	ctx, span := c.StartSpan(ctx, "ai.generate_response")
	defer span.End()

	// Set initial span attributes
	span.SetAttribute("ai.provider", "bedrock")
	span.SetAttribute("ai.prompt_length", len(prompt))

	// Apply defaults
	options = c.ApplyDefaults(options)

	// Add model to span attributes after defaults are applied
	span.SetAttribute("ai.model", options.Model)
	span.SetAttribute("ai.region", c.region)

	// Log request
	c.LogRequest("bedrock", options.Model, prompt)
	startTime := time.Now()

	// Build messages for Converse API
	messages := []types.Message{
		{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{
					Value: prompt,
				},
			},
		},
	}

	// Build the Converse input
	input := &bedrockruntime.ConverseInput{
		ModelId:  aws.String(options.Model),
		Messages: messages,
	}

	// Add system prompt if provided
	if options.SystemPrompt != "" {
		input.System = []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{
				Value: options.SystemPrompt,
			},
		}
	}

	// Add inference configuration
	inferenceConfig := &types.InferenceConfiguration{}
	configSet := false

	if options.MaxTokens > 0 {
		inferenceConfig.MaxTokens = aws.Int32(int32(options.MaxTokens))
		configSet = true
	}

	if options.Temperature > 0 {
		inferenceConfig.Temperature = aws.Float32(options.Temperature)
		configSet = true
	}

	if configSet {
		input.InferenceConfig = inferenceConfig
	}

	// Make the request to AWS Bedrock
	output, err := c.bedrockClient.Converse(ctx, input)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Bedrock request failed - converse error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "bedrock",
				"error":     err.Error(),
				"phase":     "request_execution",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("bedrock converse error: %w", err)
	}

	// Extract text content from response
	if output.Output == nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Bedrock request failed - no output", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "bedrock",
				"error":     "no_output",
				"phase":     "response_validation",
			})
		}
		noOutputErr := fmt.Errorf("no output in Bedrock response")
		span.RecordError(noOutputErr)
		return nil, noOutputErr
	}

	var content string
	switch v := output.Output.(type) {
	case *types.ConverseOutputMemberMessage:
		for _, block := range v.Value.Content {
			switch b := block.(type) {
			case *types.ContentBlockMemberText:
				content += b.Value
			}
		}
	default:
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Bedrock request failed - unexpected output type", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "bedrock",
				"error":     "unexpected_output_type",
				"phase":     "response_validation",
			})
		}
		unexpectedErr := fmt.Errorf("unexpected output type from Bedrock")
		span.RecordError(unexpectedErr)
		return nil, unexpectedErr
	}

	if content == "" {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Bedrock request failed - empty response", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "bedrock",
				"error":     "no_text_content",
				"phase":     "response_validation",
			})
		}
		emptyErr := fmt.Errorf("no text content in Bedrock response")
		span.RecordError(emptyErr)
		return nil, emptyErr
	}

	// Build the response
	result := &core.AIResponse{
		Content:  content,
		Model:    options.Model,
		Provider: "bedrock",
	}

	// Add usage information if available
	if output.Usage != nil {
		result.Usage = core.TokenUsage{
			PromptTokens:     int(*output.Usage.InputTokens),
			CompletionTokens: int(*output.Usage.OutputTokens),
			TotalTokens:      int(*output.Usage.TotalTokens),
		}
	}

	// Add token usage to span for cost tracking and debugging
	span.SetAttribute("ai.prompt_tokens", result.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", result.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", result.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(result.Content))

	// Add stop reason if available
	if output.StopReason != "" {
		span.SetAttribute("ai.stop_reason", string(output.StopReason))
	}

	// Log response
	c.LogResponse(ctx, "bedrock", result.Model, result.Usage, time.Since(startTime))
	c.LogResponseContent("bedrock", result.Model, result.Content)

	return result, nil
}

// StreamResponse generates a streaming response using AWS Bedrock's ConverseStream API
func (c *Client) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	// Start distributed tracing span for streaming
	ctx, span := c.StartSpan(ctx, "ai.stream_response")
	defer span.End()

	// Set initial span attributes
	span.SetAttribute("ai.provider", "bedrock")
	span.SetAttribute("ai.prompt_length", len(prompt))
	span.SetAttribute("ai.streaming", true)

	// Apply defaults
	options = c.ApplyDefaults(options)

	// Add model to span attributes after defaults are applied
	span.SetAttribute("ai.model", options.Model)
	span.SetAttribute("ai.region", c.region)

	// Log streaming request
	if c.Logger != nil {
		c.Logger.InfoWithContext(ctx, "Bedrock stream request initiated", map[string]interface{}{
			"operation":     "ai_stream_request",
			"provider":      "bedrock",
			"model":         options.Model,
			"prompt_length": len(prompt),
		})
	}
	startTime := time.Now()

	// Build messages for ConverseStream API
	messages := []types.Message{
		{
			Role: types.ConversationRoleUser,
			Content: []types.ContentBlock{
				&types.ContentBlockMemberText{
					Value: prompt,
				},
			},
		},
	}

	// Build the ConverseStream input
	input := &bedrockruntime.ConverseStreamInput{
		ModelId:  aws.String(options.Model),
		Messages: messages,
	}

	// Add system prompt if provided
	if options.SystemPrompt != "" {
		input.System = []types.SystemContentBlock{
			&types.SystemContentBlockMemberText{
				Value: options.SystemPrompt,
			},
		}
	}

	// Add inference configuration
	inferenceConfig := &types.InferenceConfiguration{}
	if options.MaxTokens > 0 {
		inferenceConfig.MaxTokens = aws.Int32(int32(options.MaxTokens))
	}
	if options.Temperature > 0 {
		inferenceConfig.Temperature = aws.Float32(options.Temperature)
	}
	input.InferenceConfig = inferenceConfig

	// Start the stream
	output, err := c.bedrockClient.ConverseStream(ctx, input)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Bedrock stream request failed - stream error", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "bedrock",
				"error":     err.Error(),
				"phase":     "stream_start",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("bedrock stream error: %w", err)
	}

	// Process the stream
	eventStream := output.GetStream()
	defer eventStream.Close()

	var fullContent string
	var usage core.TokenUsage
	chunkIndex := 0
	var finishReason string

	for {
		event, ok := <-eventStream.Events()
		if !ok {
			break
		}

		switch v := event.(type) {
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			if v.Value.Delta != nil {
				switch d := v.Value.Delta.(type) {
				case *types.ContentBlockDeltaMemberText:
					fullContent += d.Value

					// Create chunk and call callback
					chunk := core.StreamChunk{
						Content: d.Value,
						Delta:   true,
						Index:   chunkIndex,
						Model:   options.Model,
					}
					chunkIndex++

					if err := callback(chunk); err != nil {
						// Callback requested stop
						span.SetAttribute("ai.stream_stopped_by_callback", true)
						return &core.AIResponse{
							Content:  fullContent,
							Model:    options.Model,
							Provider: "bedrock",
							Usage:    usage,
						}, nil
					}
				}
			}

		case *types.ConverseStreamOutputMemberMetadata:
			// Capture usage from metadata
			if v.Value.Usage != nil {
				usage = core.TokenUsage{
					PromptTokens:     int(aws.ToInt32(v.Value.Usage.InputTokens)),
					CompletionTokens: int(aws.ToInt32(v.Value.Usage.OutputTokens)),
					TotalTokens:      int(aws.ToInt32(v.Value.Usage.TotalTokens)),
				}
			}

		case *types.ConverseStreamOutputMemberMessageStop:
			// Stream ended normally - capture stop reason
			finishReason = string(v.Value.StopReason)
			if c.Logger != nil {
				c.Logger.DebugWithContext(ctx, "Bedrock stream completed", map[string]interface{}{
					"operation":   "ai_stream_complete",
					"provider":    "bedrock",
					"stop_reason": finishReason,
				})
			}
			span.SetAttribute("ai.stream_completed", true)
		}

		// Check for context cancellation
		select {
		case <-ctx.Done():
			if fullContent != "" {
				span.SetAttribute("ai.stream_partial", true)
				return &core.AIResponse{
					Content:  fullContent,
					Model:    options.Model,
					Provider: "bedrock",
					Usage:    usage,
				}, core.ErrStreamPartiallyCompleted
			}
			span.RecordError(ctx.Err())
			return nil, ctx.Err()
		default:
		}
	}

	// Check for stream errors
	if err := eventStream.Err(); err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Bedrock stream error during processing", map[string]interface{}{
				"operation": "ai_stream_error",
				"provider":  "bedrock",
				"error":     err.Error(),
				"phase":     "stream_processing",
			})
		}
		// Return partial content if available
		if fullContent != "" {
			span.SetAttribute("ai.stream_partial", true)
			return &core.AIResponse{
				Content:  fullContent,
				Model:    options.Model,
				Provider: "bedrock",
				Usage:    usage,
			}, core.ErrStreamPartiallyCompleted
		}
		span.RecordError(err)
		return nil, fmt.Errorf("bedrock stream error: %w", err)
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
		Content:  fullContent,
		Model:    options.Model,
		Provider: "bedrock",
		Usage:    usage,
	}

	// Add token usage to span
	span.SetAttribute("ai.prompt_tokens", result.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", result.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", result.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(result.Content))
	span.SetAttribute("ai.chunks_sent", chunkIndex)

	// Log response
	c.LogResponse(ctx, "bedrock", result.Model, result.Usage, time.Since(startTime))
	c.LogResponseContent("bedrock", result.Model, result.Content)

	return result, nil
}

// SupportsStreaming returns true as Bedrock supports native streaming
func (c *Client) SupportsStreaming() bool {
	return true
}

// InvokeModel provides direct access to specific model APIs (for advanced use cases)
// This bypasses the Converse API and uses model-specific formats
func (c *Client) InvokeModel(ctx context.Context, modelID string, body []byte) ([]byte, error) {
	// Start distributed tracing span
	ctx, span := c.StartSpan(ctx, "ai.invoke_model")
	defer span.End()

	// Set span attributes
	span.SetAttribute("ai.provider", "bedrock")
	span.SetAttribute("ai.model", modelID)
	span.SetAttribute("ai.body_length", len(body))

	input := &bedrockruntime.InvokeModelInput{
		ModelId:     aws.String(modelID),
		Body:        body,
		ContentType: aws.String("application/json"),
		Accept:      aws.String("application/json"),
	}

	output, err := c.bedrockClient.InvokeModel(ctx, input)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Bedrock invoke model failed", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "bedrock",
				"model":     modelID,
				"error":     err.Error(),
				"phase":     "model_invocation",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("bedrock invoke model error: %w", err)
	}

	span.SetAttribute("ai.response_length", len(output.Body))
	return output.Body, nil
}

// GetEmbeddings generates embeddings using Amazon Titan Embed model
func (c *Client) GetEmbeddings(ctx context.Context, text string) ([]float32, error) {
	// Start distributed tracing span
	ctx, span := c.StartSpan(ctx, "ai.get_embeddings")
	defer span.End()

	// Set span attributes
	span.SetAttribute("ai.provider", "bedrock")
	span.SetAttribute("ai.model", ModelTitanEmbed)
	span.SetAttribute("ai.text_length", len(text))

	// Build request for Titan Embed model
	request := map[string]interface{}{
		"inputText": text,
	}

	body, err := json.Marshal(request)
	if err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Bedrock embeddings failed - marshal error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "bedrock",
				"error":     err.Error(),
				"phase":     "request_preparation",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to marshal embed request: %w", err)
	}

	// Invoke Titan Embed model
	responseBody, err := c.InvokeModel(ctx, ModelTitanEmbed, body)
	if err != nil {
		// Error already logged and recorded in InvokeModel span
		return nil, err
	}

	// Parse response
	var response struct {
		Embedding []float32 `json:"embedding"`
	}

	if err := json.Unmarshal(responseBody, &response); err != nil {
		if c.Logger != nil {
			c.Logger.ErrorWithContext(ctx, "Bedrock embeddings failed - parse response error", map[string]interface{}{
				"operation": "ai_request_error",
				"provider":  "bedrock",
				"error":     err.Error(),
				"phase":     "response_parse",
			})
		}
		span.RecordError(err)
		return nil, fmt.Errorf("failed to parse embed response: %w", err)
	}

	span.SetAttribute("ai.embedding_dimensions", len(response.Embedding))
	return response.Embedding, nil
}

// CreateAWSConfig creates an AWS configuration for Bedrock
// This can use various authentication methods:
// 1. IAM role (when running on EC2/ECS/Lambda)
// 2. AWS credentials from environment variables
// 3. AWS profile from ~/.aws/credentials
// 4. Explicit credentials passed in
func CreateAWSConfig(ctx context.Context, region string, credentials ...aws.CredentialsProvider) (aws.Config, error) {
	opts := []func(*config.LoadOptions) error{
		config.WithRegion(region),
	}

	// Add explicit credentials if provided
	if len(credentials) > 0 && credentials[0] != nil {
		opts = append(opts, config.WithCredentialsProvider(credentials[0]))
	}

	// Load the configuration
	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}

	return cfg, nil
}
