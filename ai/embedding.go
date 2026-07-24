package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/truvaagents/truva-g3/ai/providers"
	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// Compile-time interface compliance check.
var _ core.EmbeddingClient = (*EmbeddingClient)(nil)

// EmbeddingClient implements core.EmbeddingClient using any OpenAI-compatible
// embedding API endpoint (OpenAI, Ollama, Infinity, LocalAI, TEI, vLLM).
//
// Uses raw HTTP — no external SDK dependency. Follows the same pattern as
// the ai/providers/openai Client which uses direct HTTP calls.
type EmbeddingClient struct {
	baseURL    string
	apiKey     string
	model      string
	httpClient *http.Client
	logger     core.Logger
}

// EmbeddingClientOption configures EmbeddingClient.
// Returns error if the option value is invalid (fail-fast per core/ARCHITECTURE.md).
type EmbeddingClientOption func(*EmbeddingClient) error

// WithEmbeddingBaseURL sets the embedding API base URL.
// Default: "http://localhost:11434/v1" (Ollama).
func WithEmbeddingBaseURL(url string) EmbeddingClientOption {
	return func(c *EmbeddingClient) error {
		if url == "" {
			return fmt.Errorf("embedding base URL cannot be empty")
		}
		c.baseURL = url
		return nil
	}
}

// WithEmbeddingAPIKey sets the API key for authentication.
func WithEmbeddingAPIKey(key string) EmbeddingClientOption {
	return func(c *EmbeddingClient) error {
		c.apiKey = key
		return nil
	}
}

// WithEmbeddingModel sets the embedding model name.
// Default: "nomic-embed-text".
func WithEmbeddingModel(model string) EmbeddingClientOption {
	return func(c *EmbeddingClient) error {
		if model == "" {
			return fmt.Errorf("embedding model cannot be empty")
		}
		c.model = model
		return nil
	}
}

// WithEmbeddingHTTPClient sets a custom HTTP client (e.g., for TLS or tracing).
func WithEmbeddingHTTPClient(client *http.Client) EmbeddingClientOption {
	return func(c *EmbeddingClient) error {
		if client == nil {
			return fmt.Errorf("HTTP client cannot be nil")
		}
		c.httpClient = client
		return nil
	}
}

// WithEmbeddingLogger sets the logger for embedding operations.
func WithEmbeddingLogger(logger core.Logger) EmbeddingClientOption {
	return func(c *EmbeddingClient) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		c.logger = logger
		return nil
	}
}

// NewEmbeddingClient creates a new embedding client compatible with any
// OpenAI-compatible embedding endpoint.
//
// Returns error if configuration is invalid (fail-fast per FRAMEWORK_DESIGN_PRINCIPLES).
// Env var precedence: explicit options > TRUVAG3_* env vars > defaults.
func NewEmbeddingClient(opts ...EmbeddingClientOption) (*EmbeddingClient, error) {
	c := &EmbeddingClient{
		baseURL:    "http://localhost:11434/v1", // Ollama default
		model:      "nomic-embed-text",
		httpClient: &http.Client{Timeout: 30 * time.Second},
		logger:     &core.NoOpLogger{},
	}

	// Env var overrides (lower priority than explicit options)
	if url := os.Getenv("TRUVAG3_EMBEDDING_BASE_URL"); url != "" {
		c.baseURL = url
	}
	if key := os.Getenv("TRUVAG3_EMBEDDING_API_KEY"); key != "" {
		c.apiKey = key
	}
	if model := os.Getenv("TRUVAG3_EMBEDDING_MODEL"); model != "" {
		c.model = model
	}

	// Apply explicit options (highest priority)
	for _, opt := range opts {
		if err := opt(c); err != nil {
			return nil, fmt.Errorf("invalid embedding client option: %w", err)
		}
	}

	// Fail-fast validation
	if c.baseURL == "" {
		return nil, fmt.Errorf("embedding base URL cannot be empty: set TRUVAG3_EMBEDDING_BASE_URL or use WithEmbeddingBaseURL()")
	}
	if c.model == "" {
		return nil, fmt.Errorf("embedding model cannot be empty: set TRUVAG3_EMBEDDING_MODEL or use WithEmbeddingModel()")
	}
	if componentLogger, ok := c.logger.(core.ComponentAwareLogger); ok {
		c.logger = componentLogger.WithComponent("framework/ai")
	}

	return c, nil
}

func embeddingErrorTypeForStatus(statusCode int) string {
	switch {
	case statusCode == http.StatusTooManyRequests:
		return "provider_rate_limit"
	case statusCode >= http.StatusInternalServerError:
		return "provider_server"
	default:
		return "provider_client"
	}
}

func (c *EmbeddingClient) observeError(
	ctx context.Context,
	message string,
	model string,
	err error,
	fallback string,
	inputCount int,
	duration time.Duration,
	statusCode int,
) {
	errorType, safeError := providers.SanitizedObservationError(err, fallback)
	fields := map[string]interface{}{
		"operation":   "generate_embeddings",
		"provider":    "openai-compatible",
		"model":       model,
		"status":      "error",
		"error":       safeError.Error(),
		"error_type":  errorType,
		"input_count": inputCount,
		"duration_ms": duration.Milliseconds(),
	}
	if statusCode != 0 {
		fields["status_code"] = statusCode
	}
	providers.AddObservationRequestID(ctx, fields)
	c.logger.WarnWithContext(ctx, message, fields)

	telemetry.Counter("ai.embedding.errors",
		"module", telemetry.ModuleAI,
		"error_type", errorType,
	)
	requestID, _ := fields["request_id"].(string)
	attributes := []attribute.KeyValue{
		attribute.String("request_id", requestID),
		attribute.String("model", model),
		attribute.String("error", safeError.Error()),
		attribute.String("error_type", errorType),
		attribute.Int64("duration_ms", duration.Milliseconds()),
		attribute.Int("input_count", inputCount),
	}
	if statusCode != 0 {
		attributes = append(attributes, attribute.Int("status_code", statusCode))
	}
	telemetry.AddSpanEvent(ctx, "ai.embedding.error", attributes...)
}

// GenerateEmbeddings calls the /v1/embeddings endpoint and returns vector embeddings.
// Compatible with OpenAI, Ollama, Infinity, LocalAI, TEI, and vLLM.
func (c *EmbeddingClient) GenerateEmbeddings(ctx context.Context, texts []string, options *core.EmbeddingOptions) (*core.EmbeddingResponse, error) {
	if len(texts) == 0 {
		return &core.EmbeddingResponse{Embeddings: [][]float32{}}, nil
	}

	startTime := time.Now()

	model := c.model
	if options != nil && options.Model != "" {
		model = options.Model
	}

	// Build request body (OpenAI-compatible format)
	reqBody := embeddingRequest{
		Model: model,
		Input: texts,
	}
	if options != nil && options.Dimensions > 0 {
		reqBody.Dimensions = options.Dimensions
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		c.observeError(ctx, "Embedding request preparation failed", model, err, "invalid_request", len(texts), time.Since(startTime), 0)
		return nil, fmt.Errorf("failed to marshal embedding request: %w", err)
	}

	// Build HTTP request
	url := c.baseURL + "/embeddings"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		c.observeError(ctx, "Embedding request creation failed", model, err, "invalid_request", len(texts), time.Since(startTime), 0)
		return nil, fmt.Errorf("failed to create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	// Make request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.observeError(ctx, "Embedding request failed", model, err, "transport", len(texts), time.Since(startTime), 0)
		return nil, fmt.Errorf("embedding request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.observeError(ctx, "Embedding response read failed", model, err, "decode", len(texts), time.Since(startTime), resp.StatusCode)
		return nil, fmt.Errorf("failed to read embedding response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		fallback := embeddingErrorTypeForStatus(resp.StatusCode)
		providerErr := fmt.Errorf("embedding provider returned status %d", resp.StatusCode)
		c.observeError(ctx, "Embedding API error", model, providerErr, fallback, len(texts), time.Since(startTime), resp.StatusCode)
		return nil, fmt.Errorf("embedding API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	// Parse response (OpenAI-compatible format)
	var embResp embeddingResponse
	if err := json.Unmarshal(respBody, &embResp); err != nil {
		c.observeError(ctx, "Embedding response decode failed", model, err, "decode", len(texts), time.Since(startTime), resp.StatusCode)
		return nil, fmt.Errorf("failed to parse embedding response: %w", err)
	}

	// Extract embeddings in order
	embeddings := make([][]float32, len(texts))
	for _, item := range embResp.Data {
		if item.Index >= 0 && item.Index < len(embeddings) {
			embeddings[item.Index] = item.Embedding
		}
	}

	// Success telemetry — cost visibility + latency monitoring
	durationMs := time.Since(startTime).Milliseconds()
	telemetry.Counter("ai.embedding.success",
		"module", telemetry.ModuleAI,
		"status", "success",
	)
	telemetry.Histogram("ai.embedding.duration_ms", float64(durationMs),
		"module", telemetry.ModuleAI,
		"status", "success",
	)
	if embResp.Usage.TotalTokens > 0 {
		telemetry.Counter("ai.embedding.tokens_total",
			"module", telemetry.ModuleAI,
			"type", "input",
		)
		completionFields := map[string]interface{}{}
		providers.AddObservationRequestID(ctx, completionFields)
		requestID, _ := completionFields["request_id"].(string)
		telemetry.AddSpanEvent(ctx, "ai.embedding.completed",
			attribute.String("request_id", requestID),
			attribute.String("model", model),
			attribute.Int("input_count", len(texts)),
			attribute.Int("prompt_tokens", embResp.Usage.PromptTokens),
			attribute.Int("total_tokens", embResp.Usage.TotalTokens),
			attribute.Int64("duration_ms", durationMs),
		)
	}

	return &core.EmbeddingResponse{
		Embeddings: embeddings,
		Model:      embResp.Model,
		Provider:   "openai-compatible",
		Usage: core.TokenUsage{
			PromptTokens: embResp.Usage.PromptTokens,
			TotalTokens:  embResp.Usage.TotalTokens,
		},
	}, nil
}

// --- OpenAI-compatible request/response types ---

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embeddingResponse struct {
	Object string          `json:"object"`
	Data   []embeddingData `json:"data"`
	Model  string          `json:"model"`
	Usage  embeddingUsage  `json:"usage"`
}

type embeddingData struct {
	Object    string    `json:"object"`
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type embeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}
