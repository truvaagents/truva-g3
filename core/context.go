package core

import "context"

const pipelineEnrichmentsKey contextKey = "pipeline_enrichments"

// WithPipelineEnrichments stores pipeline enrichments in the context.
// Used by the orchestrator to propagate hook-injected data (e.g. RAG context,
// conversation history) to the prompt builder.
func WithPipelineEnrichments(ctx context.Context, enrichments map[string]interface{}) context.Context {
	return context.WithValue(ctx, pipelineEnrichmentsKey, enrichments)
}

// GetPipelineEnrichments retrieves pipeline enrichments from the context.
// Returns nil if no enrichments are present.
func GetPipelineEnrichments(ctx context.Context) map[string]interface{} {
	v, _ := ctx.Value(pipelineEnrichmentsKey).(map[string]interface{})
	return v
}
