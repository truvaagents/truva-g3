package ai

import (
	"context"

	"github.com/truvaagents/truva-g3/core"
)

// AIClient is the interface for AI/LLM clients
// This re-exports the core interface for convenience
type AIClient interface {
	GenerateResponse(ctx context.Context, prompt string, options *core.AIOptions) (*core.AIResponse, error)
}

// Re-export streaming types for convenience
// Consumers can import from ai module instead of directly from core
type (
	StreamChunk       = core.StreamChunk
	StreamCallback    = core.StreamCallback
	StreamingAIClient = core.StreamingAIClient
)
