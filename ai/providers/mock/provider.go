// Package mock provides a mock AI provider for testing
package mock

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/truvaagents/truva-g3/ai"
	"github.com/truvaagents/truva-g3/core"
)

func init() {
	// Register only if explicitly enabled via environment or test
	// This prevents mock from being auto-detected in production
	if err := ai.Register(&Factory{}); err != nil {
		// Panic in init() is acceptable for registration errors (caught in tests/development)
		panic(fmt.Sprintf("failed to register mock AI provider: %v", err))
	}
}

// Factory creates mock AI clients for testing
type Factory struct{}

// Name returns the provider name
func (f *Factory) Name() string {
	return "mock"
}

// Description returns provider description
func (f *Factory) Description() string {
	return "Mock provider for testing"
}

// Priority returns provider priority
func (f *Factory) Priority() int {
	return 1 // Very low priority
}

// Create creates a new mock client
func (f *Factory) Create(config *ai.AIConfig) core.AIClient {
	return NewClient(config)
}

// DetectEnvironment checks if mock is enabled
func (f *Factory) DetectEnvironment() (priority int, available bool) {
	// Mock is never auto-detected
	return 0, false
}

// Client implements core.AIClient for testing
type Client struct {
	Config        *ai.AIConfig
	Responses     []string
	ResponseIndex int
	Error         error
	CallCount     int
	LastPrompt    string
	LastOptions   *core.AIOptions

	// Streaming configuration
	ChunkSize   int           // Size of each chunk when streaming (default: 10)
	StreamDelay time.Duration // Delay between chunks (default: 0)
}

// NewClient creates a new mock client
func NewClient(config *ai.AIConfig) *Client {
	return &Client{
		Config:    config,
		Responses: []string{"Mock response"},
	}
}

// GenerateResponse returns a mock response
func (c *Client) GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error) {
	c.CallCount++
	c.LastPrompt = prompt
	c.LastOptions = options

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Return configured error if set
	if c.Error != nil {
		return nil, c.Error
	}

	// Return next response from list
	if c.ResponseIndex >= len(c.Responses) {
		return nil, errors.New("no more mock responses")
	}

	response := c.Responses[c.ResponseIndex]
	c.ResponseIndex++

	// Use options if provided, otherwise use defaults
	model := "mock-model"
	if options != nil && options.Model != "" {
		model = options.Model
	} else if c.Config != nil && c.Config.Model != "" {
		model = c.Config.Model
	}

	return &core.AIResponse{
		Content: response,
		Model:   model,
		Usage: core.TokenUsage{
			PromptTokens:     len(prompt) / 4, // Rough estimate
			CompletionTokens: len(response) / 4,
			TotalTokens:      (len(prompt) + len(response)) / 4,
		},
	}, nil
}

// StreamResponse returns a mock streaming response for testing
func (c *Client) StreamResponse(ctx context.Context, prompt string, options *core.AIOptions, callback core.StreamCallback) (*core.AIResponse, error) {
	c.CallCount++
	c.LastPrompt = prompt
	c.LastOptions = options

	// Check for context cancellation
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	// Return configured error if set
	if c.Error != nil {
		return nil, c.Error
	}

	// Return next response from list
	if c.ResponseIndex >= len(c.Responses) {
		return nil, errors.New("no more mock responses")
	}

	response := c.Responses[c.ResponseIndex]
	c.ResponseIndex++

	// Use options if provided, otherwise use defaults
	model := "mock-model"
	if options != nil && options.Model != "" {
		model = options.Model
	} else if c.Config != nil && c.Config.Model != "" {
		model = c.Config.Model
	}

	// Determine chunk size
	chunkSize := c.ChunkSize
	if chunkSize <= 0 {
		chunkSize = 10 // Default chunk size
	}

	// Stream the response in chunks
	chunkIndex := 0
	for i := 0; i < len(response); i += chunkSize {
		// Check for context cancellation
		select {
		case <-ctx.Done():
			// Return partial response
			if i > 0 {
				return &core.AIResponse{
					Content: response[:i],
					Model:   model,
					Usage: core.TokenUsage{
						PromptTokens:     len(prompt) / 4,
						CompletionTokens: i / 4,
						TotalTokens:      (len(prompt) + i) / 4,
					},
				}, core.ErrStreamPartiallyCompleted
			}
			return nil, ctx.Err()
		default:
		}

		// Calculate chunk end
		end := i + chunkSize
		if end > len(response) {
			end = len(response)
		}

		// Create and send chunk
		chunk := core.StreamChunk{
			Content: response[i:end],
			Delta:   true,
			Index:   chunkIndex,
			Model:   model,
		}
		chunkIndex++

		if err := callback(chunk); err != nil {
			// Callback requested stop - return what we have
			return &core.AIResponse{
				Content: response[:end],
				Model:   model,
				Usage: core.TokenUsage{
					PromptTokens:     len(prompt) / 4,
					CompletionTokens: end / 4,
					TotalTokens:      (len(prompt) + end) / 4,
				},
			}, nil
		}

		// Apply delay between chunks if configured
		if c.StreamDelay > 0 {
			select {
			case <-time.After(c.StreamDelay):
			case <-ctx.Done():
				return &core.AIResponse{
					Content: response[:end],
					Model:   model,
					Usage: core.TokenUsage{
						PromptTokens:     len(prompt) / 4,
						CompletionTokens: end / 4,
						TotalTokens:      (len(prompt) + end) / 4,
					},
				}, core.ErrStreamPartiallyCompleted
			}
		}
	}

	// Send final chunk with finish reason
	finalChunk := core.StreamChunk{
		Delta:        false,
		Index:        chunkIndex,
		FinishReason: "stop",
		Model:        model,
		Usage: &core.TokenUsage{
			PromptTokens:     len(prompt) / 4,
			CompletionTokens: len(response) / 4,
			TotalTokens:      (len(prompt) + len(response)) / 4,
		},
	}
	_ = callback(finalChunk)

	return &core.AIResponse{
		Content: response,
		Model:   model,
		Usage: core.TokenUsage{
			PromptTokens:     len(prompt) / 4,
			CompletionTokens: len(response) / 4,
			TotalTokens:      (len(prompt) + len(response)) / 4,
		},
	}, nil
}

// SupportsStreaming returns true as the mock provider supports streaming
func (c *Client) SupportsStreaming() bool {
	return true
}

// SetResponses sets the responses to return
func (c *Client) SetResponses(responses ...string) {
	c.Responses = responses
	c.ResponseIndex = 0
}

// SetError sets an error to return
func (c *Client) SetError(err error) {
	c.Error = err
}

// Reset resets the mock client
func (c *Client) Reset() {
	c.ResponseIndex = 0
	c.CallCount = 0
	c.LastPrompt = ""
	c.LastOptions = nil
	c.Error = nil
}
