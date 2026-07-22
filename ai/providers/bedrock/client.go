//go:build bedrock
// +build bedrock

package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/ai/requestpolicy"
	"github.com/truvaagents/truva-g3/core"
)

type runtimeClient interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
	ConverseStream(context.Context, *bedrockruntime.ConverseStreamInput, ...func(*bedrockruntime.Options)) (converseEventStream, error)
	InvokeModel(context.Context, *bedrockruntime.InvokeModelInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error)
}

type converseEventStream interface {
	Events() <-chan types.ConverseStreamOutput
	Close() error
	Err() error
}

var errInvokeModelOutputNil = errors.New("bedrock invoke model output is nil")

type sdkRuntimeClient struct{ client *bedrockruntime.Client }

func (client sdkRuntimeClient) Converse(
	ctx context.Context,
	input *bedrockruntime.ConverseInput,
	options ...func(*bedrockruntime.Options),
) (*bedrockruntime.ConverseOutput, error) {
	return client.client.Converse(ctx, input, options...)
}

func (client sdkRuntimeClient) ConverseStream(
	ctx context.Context,
	input *bedrockruntime.ConverseStreamInput,
	options ...func(*bedrockruntime.Options),
) (converseEventStream, error) {
	output, err := client.client.ConverseStream(ctx, input, options...)
	if err != nil {
		return nil, err
	}
	if output == nil {
		return nil, errors.New("bedrock ConverseStream output is nil")
	}
	return output.GetStream(), nil
}

func (client sdkRuntimeClient) InvokeModel(
	ctx context.Context,
	input *bedrockruntime.InvokeModelInput,
	options ...func(*bedrockruntime.Options),
) (*bedrockruntime.InvokeModelOutput, error) {
	return client.client.InvokeModel(ctx, input, options...)
}

// Client implements legacy and request-aware AWS Bedrock operations.
type Client struct {
	*providers.BaseClient
	bedrockClient  runtimeClient
	region         string
	requestPolicy  *requestpolicy.Engine
	requestTimeout time.Duration
}

// NewClient creates an AWS Bedrock client.
func NewClient(cfg aws.Config, region string, logger core.Logger) *Client {
	return newClientWithRuntime(sdkRuntimeClient{client: bedrockruntime.NewFromConfig(cfg)}, region, logger)
}

func newClientWithRuntime(runtime runtimeClient, region string, logger core.Logger) *Client {
	base := providers.NewBaseClient(180*time.Second, logger)
	base.ProviderName = "bedrock"
	base.DefaultModel = ModelClaude3Sonnet
	base.DefaultMaxTokens = 1000
	return &Client{
		BaseClient:     base,
		bedrockClient:  runtime,
		region:         region,
		requestPolicy:  newRequestPolicyEngine(),
		requestTimeout: 180 * time.Second,
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
		Provider:      "bedrock",
		ProviderAlias: "bedrock",
		ErrorType:     errorType,
	})
}

type preparedRequest struct {
	Model       string
	Draft       *Draft
	Report      *core.AIRequestReport
	SyncInput   *bedrockruntime.ConverseInput
	StreamInput *bedrockruntime.ConverseStreamInput
}

func newRequestPolicyEngine() *requestpolicy.Engine {
	engine, err := newRequestPolicyEngineWithIntegration(nil, nil, requestpolicy.CompatibilityCompatible)
	if err != nil {
		panic(fmt.Sprintf("invalid built-in Bedrock request policy: %v", err))
	}
	return engine
}

func newRequestPolicyEngineWithIntegration(
	appRules []core.AIProviderPatch,
	middleware []requestpolicy.RequestMiddleware,
	mode requestpolicy.CompatibilityMode,
) (*requestpolicy.Engine, error) {
	return requestpolicy.NewEngine(requestpolicy.Config{
		AppRules:   appRules,
		Middleware: middleware,
		Mode:       mode,
	})
}

func (c *Client) withRequestTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.requestTimeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.requestTimeout)
}

func (c *Client) prepareAIRequest(
	ctx context.Context,
	supplied *core.AIRequest,
	stream bool,
) (*preparedRequest, error) {
	request, err := core.CloneAIRequest(supplied)
	if err != nil {
		return nil, fmt.Errorf("clone Bedrock AI request: %w", err)
	}
	options, err := providers.CloneAIOptions(request.LegacyOptions())
	if err != nil {
		return nil, fmt.Errorf("clone Bedrock legacy request options: %w", err)
	}
	options = c.ApplyDefaults(options)
	if request.Generation.Model != "" {
		options.Model = request.Generation.Model
	}
	requestedModel := options.Model
	wireOptions := *options
	wireOptions.Model = requestedModel
	wireRequest := core.NewAIRequestFromLegacy(request.Prompt, request.Purpose, &wireOptions)
	wireRequest.Generation = request.Generation
	wireRequest.Patches = request.Patches
	var draft *Draft
	if stream {
		draft, err = NewStreamDraft(options.Model, wireRequest)
	} else {
		draft, err = NewDraft(options.Model, wireRequest)
	}
	if err != nil {
		return nil, err
	}
	prepared := &preparedRequest{Model: options.Model, Draft: draft}
	if c.requestPolicy == nil {
		return prepared, errors.New("bedrock request policy engine is not configured")
	}
	report, err := c.requestPolicy.Apply(ctx, draft, request.Patches)
	if report != nil {
		report.Adjustments = append(draft.Adjustments(), report.Adjustments...)
		prepared.Report = report
	}
	if err != nil {
		return prepared, err
	}
	if stream {
		prepared.StreamInput, err = draft.SDKStreamInput()
	} else {
		prepared.SyncInput, err = draft.SDKInput()
	}
	if err != nil {
		return prepared, fmt.Errorf("translate Bedrock logical request: %w", err)
	}
	return prepared, nil
}

// RequestFingerprint returns the stable policy identity used by AI-output
// caches. Bedrock routing is captured by the versioned Converse adapter
// identity; no credentials or SDK calls are made here.
func (c *Client) RequestFingerprint(ctx context.Context, request *core.AIRequest) (string, bool) {
	prepared, err := c.prepareAIRequest(ctx, request, false)
	if err != nil || prepared == nil || prepared.Report == nil {
		return "", false
	}
	return prepared.Report.Fingerprint, prepared.Report.Stable && prepared.Report.Fingerprint != ""
}

// GenerateResponse adapts the legacy client interface to the request-aware path.
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

// Generate applies logical policy and invokes the AWS Converse SDK operation.
func (c *Client) Generate(ctx context.Context, request *core.AIRequest) (*core.AIResult, error) {
	if request == nil {
		return nil, errors.New("bedrock AI request is nil")
	}
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := c.StartSpan(ctx, "ai.generate_response")
	defer span.End()
	span.SetAttribute("ai.provider", "bedrock")
	span.SetAttribute("ai.prompt_length", len(request.Prompt))
	span.SetAttribute("ai.region", c.region)

	prepared, err := c.prepareAIRequest(ctx, request, false)
	if err != nil {
		if prepared != nil {
			c.recordPreparation(span, prepared)
		}
		c.observeError(ctx, span, "ai_request", "policy", err)
		return resultWithReport(prepared, nil), err
	}
	c.recordPreparation(span, prepared)
	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "bedrock",
		ProviderAlias: "bedrock",
		SemanticModel: prepared.Model,
		PromptLength:  len(request.Prompt),
	})
	started := time.Now()
	output, err := c.bedrockClient.Converse(ctx, prepared.SyncInput)
	if err != nil {
		c.observeError(ctx, span, "ai_request", "transport", err)
		return resultWithReport(prepared, nil), fmt.Errorf("bedrock converse error: %w", err)
	}
	result, err := decodeConverseOutput(prepared.Model, output)
	if err != nil {
		c.observeError(ctx, span, "ai_request", "decode", err)
		return resultWithReport(prepared, nil), err
	}
	c.recordResponse(ctx, span, prepared.Model, result.Response, started)
	if output.StopReason != "" {
		span.SetAttribute("ai.stop_reason", string(output.StopReason))
	}
	return resultWithReport(prepared, result), nil
}

// StreamResponse adapts legacy streaming to the request-aware path.
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

// Stream applies the same logical policy and invokes ConverseStream.
func (c *Client) Stream(
	ctx context.Context,
	request *core.AIRequest,
	callback core.StreamCallback,
) (*core.AIResult, error) {
	if request == nil {
		return nil, errors.New("bedrock AI request is nil")
	}
	if callback == nil {
		return nil, errors.New("bedrock stream callback is nil")
	}
	ctx, cancel := c.withRequestTimeout(ctx)
	defer cancel()
	ctx, span := c.StartSpan(ctx, "ai.stream_response")
	defer span.End()
	span.SetAttribute("ai.provider", "bedrock")
	span.SetAttribute("ai.prompt_length", len(request.Prompt))
	span.SetAttribute("ai.streaming", true)
	span.SetAttribute("ai.region", c.region)

	prepared, err := c.prepareAIRequest(ctx, request, true)
	if err != nil {
		if prepared != nil {
			c.recordPreparation(span, prepared)
		}
		c.observeError(ctx, span, "ai_stream", "policy", err)
		return resultWithReport(prepared, nil), err
	}
	c.recordPreparation(span, prepared)
	c.LogRequestMetadata(ctx, providers.RequestObservation{
		Provider:      "bedrock",
		ProviderAlias: "bedrock",
		SemanticModel: prepared.Model,
		PromptLength:  len(request.Prompt),
	})
	started := time.Now()
	eventStream, err := c.bedrockClient.ConverseStream(ctx, prepared.StreamInput)
	if err != nil {
		c.observeError(ctx, span, "ai_stream", "transport", err)
		return resultWithReport(prepared, nil), fmt.Errorf("bedrock stream error: %w", err)
	}
	if eventStream == nil {
		err := errors.New("bedrock stream output is nil")
		c.observeError(ctx, span, "ai_stream", "decode", err)
		return resultWithReport(prepared, nil), err
	}
	defer func() { _ = eventStream.Close() }()

	var content string
	var usage core.TokenUsage
	var usageDetails *core.AIUsageDetails
	chunkIndex := 0
	var finishReason string
	for event := range eventStream.Events() {
		switch value := event.(type) {
		case *types.ConverseStreamOutputMemberContentBlockDelta:
			if delta, ok := value.Value.Delta.(*types.ContentBlockDeltaMemberText); ok {
				content += delta.Value
				if callback(core.StreamChunk{
					Content: delta.Value, Delta: true, Index: chunkIndex, Model: prepared.Model,
				}) != nil {
					span.SetAttribute("ai.stream_stopped_by_callback", true)
					span.SetAttribute("ai.stream_status", "callback_stop")
					result := &core.AIResult{
						Response:     responseFor(content, prepared.Model, usage),
						UsageDetails: usageDetails,
					}
					c.recordResponse(ctx, span, prepared.Model, result.Response, started)
					span.SetAttribute("ai.chunks_sent", chunkIndex+1)
					return resultWithReport(prepared, result), nil
				}
				chunkIndex++
			}
		case *types.ConverseStreamOutputMemberMetadata:
			if value.Value.Usage != nil {
				usage, usageDetails = normalizeUsage(value.Value.Usage)
			}
		case *types.ConverseStreamOutputMemberMessageStop:
			finishReason = string(value.Value.StopReason)
		}
		select {
		case <-ctx.Done():
			if content != "" {
				streamErr := core.ErrStreamPartiallyCompleted
				c.observeError(ctx, span, "ai_stream", "partial_stream", streamErr)
				result := &core.AIResult{
					Response:     responseFor(content, prepared.Model, usage),
					UsageDetails: usageDetails,
				}
				return resultWithReport(prepared, result), streamErr
			}
			ctxErr := ctx.Err()
			c.observeError(ctx, span, "ai_stream", "cancelled", ctxErr)
			return resultWithReport(prepared, nil), ctxErr
		default:
		}
	}
	if err := eventStream.Err(); err != nil {
		if content != "" {
			streamErr := core.ErrStreamPartiallyCompleted
			c.observeError(ctx, span, "ai_stream", "partial_stream", streamErr)
			result := &core.AIResult{
				Response:     responseFor(content, prepared.Model, usage),
				UsageDetails: usageDetails,
			}
			return resultWithReport(prepared, result), streamErr
		}
		c.observeError(ctx, span, "ai_stream", "transport", err)
		return resultWithReport(prepared, nil), fmt.Errorf("bedrock stream error: %w", err)
	}
	if finishReason != "" {
		_ = callback(core.StreamChunk{
			Delta: false, Index: chunkIndex, FinishReason: finishReason,
			Model: prepared.Model, Usage: &usage,
		})
	}
	result := &core.AIResult{
		Response:     responseFor(content, prepared.Model, usage),
		UsageDetails: usageDetails,
	}
	c.recordResponse(ctx, span, prepared.Model, result.Response, started)
	span.SetAttribute("ai.chunks_sent", chunkIndex)
	return resultWithReport(prepared, result), nil
}

func decodeConverseOutput(model string, output *bedrockruntime.ConverseOutput) (*core.AIResult, error) {
	if output == nil || output.Output == nil {
		return nil, errors.New("no output in Bedrock response")
	}
	message, ok := output.Output.(*types.ConverseOutputMemberMessage)
	if !ok {
		return nil, errors.New("unexpected output type from Bedrock")
	}
	var content string
	for _, block := range message.Value.Content {
		if text, ok := block.(*types.ContentBlockMemberText); ok {
			content += text.Value
		}
	}
	if content == "" {
		return nil, errors.New("no text content in Bedrock response")
	}
	usage, usageDetails := normalizeUsage(output.Usage)
	return &core.AIResult{
		Response:     responseFor(content, model, usage),
		UsageDetails: usageDetails,
	}, nil
}

func normalizeUsage(usage *types.TokenUsage) (core.TokenUsage, *core.AIUsageDetails) {
	if usage == nil {
		return core.TokenUsage{}, nil
	}
	normalized := core.TokenUsage{
		PromptTokens:     int(aws.ToInt32(usage.InputTokens)),
		CompletionTokens: int(aws.ToInt32(usage.OutputTokens)),
		TotalTokens:      int(aws.ToInt32(usage.TotalTokens)),
	}
	cacheRead := int64(aws.ToInt32(usage.CacheReadInputTokens))
	cacheWrite := int64(aws.ToInt32(usage.CacheWriteInputTokens))
	if cacheRead == 0 && cacheWrite == 0 {
		return normalized, nil
	}
	details := &core.AIUsageDetails{CachedInputTokens: cacheRead}
	if cacheWrite != 0 {
		details.Counters = map[string]int64{"cache_write_input_tokens": cacheWrite}
	}
	return normalized, details
}

func responseFor(content, model string, usage core.TokenUsage) *core.AIResponse {
	return &core.AIResponse{Content: content, Model: model, Provider: "bedrock", Usage: usage}
}

func (c *Client) recordPreparation(span core.Span, prepared *preparedRequest) {
	if prepared == nil {
		return
	}
	span.SetAttribute("ai.model", prepared.Model)
	if prepared.Report != nil {
		span.SetAttribute("ai.request.provider_alias", prepared.Report.ProviderAlias)
		span.SetAttribute("ai.request.surface", prepared.Report.Surface)
		span.SetAttribute("ai.request.operation", prepared.Report.Operation)
		span.SetAttribute("ai.request.policy_stable", prepared.Report.Stable)
		span.SetAttribute("ai.request.adjustment_count", len(prepared.Report.Adjustments))
		if prepared.Report.Fingerprint != "" {
			span.SetAttribute("ai.request.policy_fingerprint", prepared.Report.Fingerprint)
		}
	}
}

func (c *Client) recordResponse(
	ctx context.Context,
	span core.Span,
	semanticModel string,
	response *core.AIResponse,
	started time.Time,
) {
	span.SetAttribute("ai.prompt_tokens", response.Usage.PromptTokens)
	span.SetAttribute("ai.completion_tokens", response.Usage.CompletionTokens)
	span.SetAttribute("ai.total_tokens", response.Usage.TotalTokens)
	span.SetAttribute("ai.response_length", len(response.Content))
	c.LogResponseMetadata(ctx, providers.ResponseObservation{
		Provider:      "bedrock",
		ProviderAlias: "bedrock",
		SemanticModel: semanticModel,
		Usage:         response.Usage,
		Duration:      time.Since(started),
	})
}

func resultWithReport(prepared *preparedRequest, result *core.AIResult) *core.AIResult {
	if result == nil {
		if prepared == nil || prepared.Report == nil {
			return nil
		}
		result = &core.AIResult{}
	}
	if prepared != nil {
		result.RequestReport = prepared.Report
	}
	return result
}

func (c *Client) SupportsStreaming() bool { return true }

// InvokeModel provides direct access to model-specific Bedrock payloads.
func (c *Client) InvokeModel(ctx context.Context, modelID string, body []byte) ([]byte, error) {
	ctx, span := c.StartSpan(ctx, "ai.invoke_model")
	defer span.End()
	span.SetAttribute("ai.provider", "bedrock")
	span.SetAttribute("ai.model", modelID)
	span.SetAttribute("ai.body_length", len(body))
	output, err := c.bedrockClient.InvokeModel(ctx, &bedrockruntime.InvokeModelInput{
		ModelId: aws.String(modelID), Body: body,
		ContentType: aws.String("application/json"), Accept: aws.String("application/json"),
	})
	if err != nil {
		c.observeError(ctx, span, "ai_invoke_model", "transport", err)
		return nil, fmt.Errorf("bedrock invoke model error: %w", err)
	}
	if output == nil {
		c.observeError(ctx, span, "ai_invoke_model", "decode", errInvokeModelOutputNil)
		return nil, errInvokeModelOutputNil
	}
	span.SetAttribute("ai.response_length", len(output.Body))
	return output.Body, nil
}

// GetEmbeddings generates embeddings using Amazon Titan Embed.
func (c *Client) GetEmbeddings(ctx context.Context, text string) ([]float32, error) {
	ctx, span := c.StartSpan(ctx, "ai.get_embeddings")
	defer span.End()
	span.SetAttribute("ai.provider", "bedrock")
	span.SetAttribute("ai.model", ModelTitanEmbed)
	span.SetAttribute("ai.text_length", len(text))

	body, err := json.Marshal(map[string]interface{}{"inputText": text})
	if err != nil {
		c.observeError(ctx, span, "ai_get_embeddings", "invalid_request", err)
		return nil, fmt.Errorf("marshal Bedrock embed request: %w", err)
	}
	responseBody, err := c.InvokeModel(ctx, ModelTitanEmbed, body)
	if err != nil {
		// InvokeModel owns the detailed error log; the outer embedding span still
		// records the propagated failure so every failed operation is visible.
		fallback := "transport"
		if errors.Is(err, errInvokeModelOutputNil) {
			fallback = "decode"
		}
		providers.RecordObservationError(span, err, fallback)
		return nil, err
	}
	var response struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.Unmarshal(responseBody, &response); err != nil {
		c.observeError(ctx, span, "ai_get_embeddings", "decode", err)
		return nil, fmt.Errorf("parse Bedrock embed response: %w", err)
	}
	span.SetAttribute("ai.embedding_dimensions", len(response.Embedding))
	return response.Embedding, nil
}

// CreateAWSConfig loads AWS region and credentials for Bedrock.
func CreateAWSConfig(ctx context.Context, region string, credentials ...aws.CredentialsProvider) (aws.Config, error) {
	options := []func(*config.LoadOptions) error{config.WithRegion(region)}
	if len(credentials) > 0 && credentials[0] != nil {
		options = append(options, config.WithCredentialsProvider(credentials[0]))
	}
	configuration, err := config.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load AWS config: %w", err)
	}
	return configuration, nil
}

var _ core.AIRequestClient = (*Client)(nil)
var _ core.StreamingAIRequestClient = (*Client)(nil)
