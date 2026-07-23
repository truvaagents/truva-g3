//go:build bedrock
// +build bedrock

package bedrock

const (
	// ModelClaudeSonnet5 is the current direct-inference default. Geographic
	// and global inference-profile IDs are explicit routing choices and are not
	// silently substituted for this model ID.
	ModelClaudeSonnet5 = "anthropic.claude-sonnet-5"

	// ModelTitanEmbedV2 is the default model used by GetEmbeddings.
	ModelTitanEmbedV2 = "amazon.titan-embed-text-v2:0"

	// ModelTitanEmbedV1 preserves the 1536-dimensional Titan Text Embeddings V1
	// route for applications that must remain compatible with an existing V1
	// vector store. GetEmbeddings does not select it implicitly.
	ModelTitanEmbedV1 = "amazon.titan-embed-text-v1"
)
