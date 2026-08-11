// Package orchestration provides intelligent parameter binding for multi-step workflows.
// This file implements micro-resolution: focused LLM calls to extract parameters
// from source data when auto-wiring cannot find a match.
package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// FunctionCallingClient extends the basic AIClient with function calling support.
// Providers that support OpenAI-style function calling should implement this interface.
type FunctionCallingClient interface {
	core.AIClient

	// ChatWithFunctions sends a message with function definitions and returns
	// either a text response or a function call result
	ChatWithFunctions(ctx context.Context, messages []ChatMessage, functions []FunctionDef) (*FunctionCallResponse, error)
}

// ChatMessage represents a message in a chat conversation
type ChatMessage struct {
	Role    string `json:"role"` // "system", "user", "assistant"
	Content string `json:"content"`
}

// FunctionDef defines a function for LLM function calling
type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"` // JSON Schema
}

// ToolCallResult is the result of a function call from the LLM
type ToolCallResult struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

// FunctionCallResponse is the response from ChatWithFunctions
type FunctionCallResponse struct {
	Content  string          // Text response (if no function call)
	ToolCall *ToolCallResult // Function call result (if any)
}

// promptTemplateReserve is the number of bytes reserved for the prompt template
// that wraps around source data in micro-resolution and semantic retry prompts.
// This ensures the total prompt (source data + template) stays within the configured
// budget, preventing Cloudflare 400 rejections when source data is trimmed to exactly
// the budget limit. 2 KB covers all template variants generously:
//   - Function-calling template: ~200 bytes
//   - Text extraction template: ~500 bytes
//   - Semantic retry template: ~800 bytes (includes error context, capability schema)
const promptTemplateReserve = 2048

// defaultSchemaGuidedMappingThreshold is the source data size (bytes) above which
// schema-guided mapping is used instead of value extraction. Below this, the full
// source data fits comfortably in the micro-resolution prompt with no data loss.
// Configurable via ResultTrimConfig.SchemaGuidedMappingThreshold.
const defaultSchemaGuidedMappingThreshold = 16384 // 16 KB

// maxSummarySamples is the number of array items included as samples in the
// structural summary. 2 items shows structure + variety without bloating.
const maxSummarySamples = 2

// maxScalarSchemaBytes is the maximum byte length for a string scalar value
// embedded in the structural summary schema. Strings exceeding this are replaced
// with a type+size descriptor (e.g., "string(535213 bytes)"). This enforces the
// design invariant that summaries remain ~1-3KB regardless of input size (§10.5.3).
// 256 bytes covers any practical URL, identifier, or short text needed for
// template interpolation and literal inference.
const maxScalarSchemaBytes = 256

// minSiblingGroupSize is the minimum number of structurally identical siblings
// required before they are collapsed into a single grouped entry (§13).
const minSiblingGroupSize = 3

// maxGroupedKeys is the maximum number of original key names listed in a
// grouped entry's _keys field. Beyond this, a "...+N more" sentinel is appended.
const maxGroupedKeys = 10

// MappingSource identifies how a parameter value should be obtained.
type MappingSource string

const (
	MappingSourcePath     MappingSource = "path"     // Reference a value in source data by path
	MappingSourceLiteral  MappingSource = "literal"  // Constant value inferred from context
	MappingSourceTemplate MappingSource = "template" // String interpolation from source data
)

// MappingTransform identifies an optional transformation on the referenced value.
type MappingTransform string

const (
	MappingTransformNone      MappingTransform = ""          // No transformation (forward as-is)
	MappingTransformSerialize MappingTransform = "serialize" // JSON-stringify the value
	MappingTransformJoin      MappingTransform = "join"      // Join array items with "\n"
	MappingTransformFirst     MappingTransform = "first"     // Take first item from array
	MappingTransformCount     MappingTransform = "count"     // Count array length → integer
)

// MappingExpr is a single parameter mapping instruction produced by the LLM.
// The LLM sees a structural summary of the source data and produces these
// expressions to tell the executor WHERE the data is and HOW to transform it.
type MappingExpr struct {
	Source    MappingSource    `json:"source"`              // "path", "literal", or "template"
	Path      string           `json:"path,omitempty"`      // JSONPath-like reference (for source=path)
	Fields    []string         `json:"fields,omitempty"`    // Sub-field projection (for arrays of objects)
	Transform MappingTransform `json:"transform,omitempty"` // Optional transformation
	Value     interface{}      `json:"value,omitempty"`     // Constant value (for source=literal)
	Template  string           `json:"template,omitempty"`  // String interpolation (for source=template)
}

// MicroResolver resolves parameters using focused LLM calls
type MicroResolver struct {
	// aiClient is the basic AI client for text-based resolution
	aiClient core.AIClient
	// functionClient is the optional function-calling client for typed resolution
	functionClient FunctionCallingClient
	// logger for debugging
	logger core.Logger

	// Result processing for large source data trimming (Phase 5).
	// When set, source data exceeding maxSourceDataBytes is trimmed before
	// embedding in LLM prompts, preventing Cloudflare 400 on large responses.
	resultProcessor    ResultProcessor
	maxSourceDataBytes int // Default: 16384 (16 KB)

	// Schema-guided mapping threshold (bytes). Source data above this size uses
	// schema-guided mapping instead of value extraction. Default: 16384 (16 KB).
	// Configurable via ResultTrimConfig.SchemaGuidedMappingThreshold or
	// TRUVAG3_RESULT_TRIM_SCHEMA_MAPPING_THRESHOLD env var. Set to 0 to disable.
	schemaMappingThreshold int

	// Max output tokens for micro-resolution LLM calls. Default: 2000.
	// Configurable via TRUVAG3_MICRO_RESOLUTION_MAX_TOKENS env var or
	// WithMicroResolutionMaxTokens() option.
	maxTokens int

	// Per-phase AI options override for micro-resolution LLM calls.
	aiOptionsOverride *AIOptionsOverride
	model             string

	// Telemetry for distributed tracing (spans for schema_mapping / value_extraction)
	telemetry core.Telemetry

	// LLM Debug Store for full payload visibility
	debugStore LLMDebugStore
	debugWg    sync.WaitGroup
	debugSeqID atomic.Uint64
}

// NewMicroResolver creates a new micro-resolver
func NewMicroResolver(aiClient core.AIClient, logger core.Logger) *MicroResolver {
	mr := &MicroResolver{
		aiClient:               aiClient,
		logger:                 logger,
		maxTokens:              2000, // Default: 2000 (was hardcoded 500)
		schemaMappingThreshold: defaultSchemaGuidedMappingThreshold,
	}

	// Check if the client supports function calling
	if fc, ok := aiClient.(FunctionCallingClient); ok {
		mr.functionClient = fc
	}

	return mr
}

// SetTelemetry sets the telemetry provider for distributed tracing.
// When set, resolveWithText and resolveWithSchemaMapping create child spans
// visible in Jaeger (micro_resolver.value_extraction / micro_resolver.schema_mapping).
func (m *MicroResolver) SetTelemetry(t core.Telemetry) {
	m.telemetry = t
}

// SetAIOptionsOverride sets the per-phase AI options override for micro-resolution calls.
func (m *MicroResolver) SetAIOptionsOverride(opts *AIOptionsOverride) {
	m.aiOptionsOverride = opts
	merged := mergeAIOptions(&core.AIOptions{
		Temperature: 0.0,
		MaxTokens:   m.maxTokens,
	}, opts)
	m.maxTokens = merged.MaxTokens
	m.model = merged.Model
}

// Deprecated compatibility setters kept while tests/examples migrate.
func (m *MicroResolver) SetModel(model string) {
	m.model = model
	if m.aiOptionsOverride == nil {
		m.aiOptionsOverride = &AIOptionsOverride{}
	}
	m.aiOptionsOverride.Model = StringPtr(model)
}

func (m *MicroResolver) SetMaxTokens(maxTokens int) {
	if maxTokens <= 0 {
		return
	}
	m.maxTokens = maxTokens
	if m.aiOptionsOverride == nil {
		m.aiOptionsOverride = &AIOptionsOverride{}
	}
	m.aiOptionsOverride.MaxTokens = IntPtr(maxTokens)
}

// ResolveParameters extracts parameters from source data for a target capability.
// If function calling is available, uses it for guaranteed type safety.
// Otherwise, falls back to text-based extraction with JSON parsing.
//
// The stepID parameter associates any LLM calls with the execution step for
// DAG visualization. Pass empty string if not step-specific.
func (m *MicroResolver) ResolveParameters(
	ctx context.Context,
	sourceData map[string]interface{},
	targetCapability *EnhancedCapability,
	hint string,
	stepID string,
) (map[string]interface{}, error) {
	// Check source data size for schema-guided mapping threshold.
	// Only serialize for size check when threshold is configured (> 0).
	if m.schemaMappingThreshold > 0 {
		sourceJSON, _ := json.Marshal(sourceData)
		if len(sourceJSON) > m.schemaMappingThreshold {
			m.logInfo("Source data exceeds schema-mapping threshold, using schema-guided mapping", map[string]interface{}{
				"capability":   targetCapability.Name,
				"source_bytes": len(sourceJSON),
				"threshold":    m.schemaMappingThreshold,
				"step_id":      stepID,
			})
			result, err := m.resolveWithSchemaMapping(ctx, sourceData, targetCapability, hint, stepID)
			if err != nil {
				// Fallback to existing path on mapping failure
				m.logWarn("Schema-guided mapping failed, falling back to value extraction", map[string]interface{}{
					"error":      err.Error(),
					"capability": targetCapability.Name,
					"step_id":    stepID,
				})
				// Fall through to existing resolution
			} else {
				return result, nil
			}
		}
	}

	// Existing path: value extraction (small data or fallback)
	if m.functionClient != nil {
		return m.resolveWithFunctions(ctx, sourceData, targetCapability, hint, stepID)
	}
	return m.resolveWithText(ctx, sourceData, targetCapability, hint, stepID)
}

// resolveWithFunctions uses LLM function calling for typed parameter extraction
func (m *MicroResolver) resolveWithFunctions(
	ctx context.Context,
	sourceData map[string]interface{},
	targetCapability *EnhancedCapability,
	hint string,
	stepID string,
) (map[string]interface{}, error) {
	// Distributed tracing: create span for function-calling LLM call
	if m.telemetry != nil {
		var span core.Span
		ctx, span = m.telemetry.StartSpan(ctx, "micro_resolver.function_calling")
		defer span.End()
		span.SetAttribute("step.id", stepID)
		span.SetAttribute("resolution.method", "function_calling")
		span.SetAttribute("params.count", len(targetCapability.Parameters))
	}

	// Build the JSON schema for the target parameters
	schema := m.buildParameterSchema(targetCapability)

	// Build the prompt (compact JSON for accurate budget accounting)
	sourceJSON, _ := json.Marshal(sourceData)
	sourceJSON = m.trimSourceData(ctx, sourceJSON, targetCapability.Name, hint, stepID)

	prompt := fmt.Sprintf(`Extract the parameters needed for the "%s" function.

Available data from previous step:
%s

%s

Return the extracted parameter values using the provided function.`,
		targetCapability.Name,
		string(sourceJSON),
		hint,
	)

	// Define the function for extraction
	function := FunctionDef{
		Name:        "provide_parameters",
		Description: fmt.Sprintf("Provide parameters for %s", targetCapability.Name),
		Parameters:  schema,
	}

	m.logDebug("Micro-resolution using function calling", map[string]interface{}{
		"capability":  targetCapability.Name,
		"source_keys": getMapKeys(sourceData),
	})

	// Make the LLM call
	resp, err := m.functionClient.ChatWithFunctions(ctx,
		[]ChatMessage{{Role: "user", Content: prompt}},
		[]FunctionDef{function},
	)
	if err != nil {
		return nil, fmt.Errorf("micro-resolution failed: %w", err)
	}

	if resp.ToolCall == nil {
		return nil, fmt.Errorf("LLM did not return a function call")
	}

	m.logInfo("Micro-resolution completed via function calling", map[string]interface{}{
		"capability":      targetCapability.Name,
		"resolved_params": resp.ToolCall.Arguments,
	})

	return resp.ToolCall.Arguments, nil
}

// resolveWithText uses text-based LLM extraction as fallback
func (m *MicroResolver) resolveWithText(
	ctx context.Context,
	sourceData map[string]interface{},
	targetCapability *EnhancedCapability,
	hint string,
	stepID string,
) (map[string]interface{}, error) {
	// Distributed tracing: create span for value extraction LLM call
	if m.telemetry != nil {
		var span core.Span
		ctx, span = m.telemetry.StartSpan(ctx, "micro_resolver.value_extraction")
		defer span.End()
		span.SetAttribute("step.id", stepID)
		span.SetAttribute("resolution.method", "value_extraction")
		span.SetAttribute("params.count", len(targetCapability.Parameters))
	}

	// Compact JSON for accurate budget accounting (indented JSON inflates size by ~30%,
	// causing false-positive trimming for responses in the 100-128KB range)
	sourceJSON, _ := json.Marshal(sourceData)
	sourceJSON = m.trimSourceData(ctx, sourceJSON, targetCapability.Name, hint, stepID)

	// Build parameter descriptions
	var paramDescs []string
	for _, p := range targetCapability.Parameters {
		paramDescs = append(paramDescs, fmt.Sprintf("- %s (%s): %s", p.Name, p.Type, p.Description))
	}

	prompt := fmt.Sprintf(`<identity>
You are a parameter extraction assistant. Extract values from source data to fill required parameters.
</identity>

<instructions>
1. Find values in the source data that match each required parameter
2. Convert types as needed (e.g., string "48.85" to number 48.85)
3. Return only a valid JSON object with the extracted parameters
</instructions>

<source_data>
%s
</source_data>

<target_parameters capability="%s">
%s
</target_parameters>

%s

Return extracted parameters as JSON. Output raw JSON — no markdown, no code blocks. Start with { and end with }.
Example: {"latitude": 48.85, "longitude": 2.35}`,
		string(sourceJSON),
		targetCapability.Name,
		strings.Join(paramDescs, "\n"),
		hint,
	)

	m.logDebug("Micro-resolution using text extraction", map[string]interface{}{
		"capability":  targetCapability.Name,
		"source_keys": getMapKeys(sourceData),
	})

	opts := mergeAIOptions(&core.AIOptions{
		Temperature: 0.0, // Deterministic for extraction
		MaxTokens:   m.maxTokens,
	}, m.aiOptionsOverride)

	// Telemetry: Record LLM prompt for micro-resolution
	microEventAttrs := []attribute.KeyValue{
		attribute.String("capability", targetCapability.Name),
		attribute.String("prompt", truncateString(prompt, 1500)),
		attribute.Int("prompt_length", len(prompt)),
		attribute.String("hint", hint),
	}
	if opts.Model != "" {
		microEventAttrs = append(microEventAttrs, attribute.String("model", opts.Model))
	}
	telemetry.AddSpanEvent(ctx, "llm.micro_resolution.request", microEventAttrs...)

	// Get request ID from context baggage for debug correlation
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = m.generateFallbackRequestID()
	}

	// Make the LLM call
	llmStartTime := time.Now()
	invocation := aiInvocation{
		Purpose:        "micro-resolution",
		Prompt:         prompt,
		Options:        opts,
		DeferRecording: m.debugStore != nil,
	}
	invocationResult, err := invokeAI(ctx, m.aiClient, invocation)
	var resp *core.AIResponse
	if invocationResult != nil {
		resp = invocationResult.Response
	}
	llmDuration := time.Since(llmStartTime)
	if err == nil {
		core.RecordTokenUsage(ctx, "micro_resolution", resp.Usage)
	}

	if err != nil {
		telemetry.AddSpanEvent(ctx, "llm.micro_resolution.error",
			attribute.String("capability", targetCapability.Name),
			attribute.String("error", err.Error()),
			attribute.Int64("duration_ms", llmDuration.Milliseconds()),
		)

		// LLM Debug: Record failed micro-resolution attempt
		m.recordDebugInteraction(ctx, requestID, withEffectiveAIRequest(LLMInteraction{
			Type:       "micro_resolution",
			Timestamp:  llmStartTime,
			DurationMs: llmDuration.Milliseconds(),
			Success:    false,
			Error:      err.Error(),
			Attempt:    1,
			StepID:     stepID,
		}, invocationResult, invocation, resp, err))

		return nil, fmt.Errorf("micro-resolution text call failed: %w", err)
	}

	// Telemetry: Record LLM response for micro-resolution
	telemetry.AddSpanEvent(ctx, "llm.micro_resolution.response",
		attribute.String("capability", targetCapability.Name),
		attribute.String("response", truncateString(resp.Content, 1000)),
		attribute.Int("response_length", len(resp.Content)),
		attribute.Int64("duration_ms", llmDuration.Milliseconds()),
	)

	// LLM Debug: Record successful micro-resolution
	m.recordDebugInteraction(ctx, requestID, withEffectiveAIRequest(LLMInteraction{
		Type:             "micro_resolution",
		Timestamp:        llmStartTime,
		DurationMs:       llmDuration.Milliseconds(),
		Response:         resp.Content,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		Success:          true,
		Attempt:          1,
		StepID:           stepID,
	}, invocationResult, invocation, resp, nil))

	// Parse the JSON response
	content := strings.TrimSpace(resp.Content)
	// Remove markdown code blocks if present
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(content), &result); err != nil {
		m.logWarn("Failed to parse micro-resolution response", map[string]interface{}{
			"error":    err.Error(),
			"response": content,
		})
		return nil, fmt.Errorf("failed to parse LLM response as JSON: %w", err)
	}

	// Apply type coercion to match target types
	for _, param := range targetCapability.Parameters {
		if val, ok := result[param.Name]; ok {
			result[param.Name] = coerceType(val, param.Type)
		}
	}

	m.logInfo("Micro-resolution completed via text extraction", map[string]interface{}{
		"capability":      targetCapability.Name,
		"resolved_params": result,
	})

	return result, nil
}

// resolveWithSchemaMapping uses schema-guided mapping for large source data.
// Instead of sending trimmed data to the LLM for value extraction, sends a compact
// structural summary and asks the LLM for mapping instructions. Then applies those
// mappings to the FULL, untrimmed source data.
//
// Flow:
//  1. generateStructuralSummary(sourceData) → 1-3KB summary
//  2. LLM: "Here's the data STRUCTURE. How should I map these parameters?" → MappingExpr per param
//  3. applyMapping(FULL sourceData, mapping) → parameter values with zero data loss
func (m *MicroResolver) resolveWithSchemaMapping(
	ctx context.Context,
	sourceData map[string]interface{},
	targetCapability *EnhancedCapability,
	hint string,
	stepID string,
) (map[string]interface{}, error) {
	// Distributed tracing: create span for schema mapping LLM call
	if m.telemetry != nil {
		var span core.Span
		ctx, span = m.telemetry.StartSpan(ctx, "micro_resolver.schema_mapping")
		defer span.End()
		span.SetAttribute("step.id", stepID)
		span.SetAttribute("resolution.method", "schema_mapping")
	}

	// Step 1: Generate structural summary (~1-3KB regardless of input size)
	summary := generateStructuralSummary(sourceData)
	summaryJSON, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to generate structural summary: %w", err)
	}

	// Step 2: Build parameter descriptions (same as resolveWithText)
	var paramDescs []string
	for _, p := range targetCapability.Parameters {
		reqStr := ""
		if p.Required {
			reqStr = " (REQUIRED)"
		}
		paramDescs = append(paramDescs, fmt.Sprintf("- %s (%s)%s: %s", p.Name, p.Type, reqStr, p.Description))
	}

	// Step 3: Build the schema-mapping prompt (structured per §8.5 checklist)
	prompt := fmt.Sprintf(`<identity>
You are a parameter mapping assistant. Given a structural summary of source data, produce mapping expressions that tell the executor how to extract each parameter value from the full source data.
</identity>

<instructions>
1. Produce one mapping expression per target parameter.
2. Three source types are available:
   - "path": reference a value by dot-path, e.g. {"source": "path", "path": "data.results"}
   - "literal": a constant value, e.g. {"source": "literal", "value": "summary"}
   - "template": string interpolation, e.g. {"source": "template", "template": "Report for {data.symbol}"}
3. Path options:
   - "fields": project specific sub-fields from objects, e.g. "fields": ["title", "body"]
   - "transform": apply a transform — "serialize" (JSON-stringify), "join" (concatenate with newlines), "first" (first array item), "count" (array length as integer)
4. For bulk-data parameters (arrays, lists), reference the entire array container using "path".
5. Reference paths exactly as they appear in the schema — the executor applies your mappings to the full source data.
6. Schema may contain grouped entries (marked with "_grouped": true) where multiple siblings share identical structure.
   - When a map node directly contains "_grouped": true, ALL its children share that structure.
   - When a map node contains "_group_0", "_group_1" etc., it has MULTIPLE groups of siblings.
     Each "_group_N" entry lists its members in "_keys". Non-grouped children appear as regular entries.
   In both cases, each child is individually addressable by its original key name from "_keys".
   For example, if "_keys" includes "bookValue", the path "data.series.annual.bookValue" references that child.
   To reference all siblings, use the parent path (e.g., "data.series.annual").
</instructions>

<example>
Summary:
{"schema":{"data":{"articles":[{"headline":"string","body":"string","source":"string"}],"topic":"Technology"}},"samples":{"data.articles[0]":{"headline":"AI Advances","body":"Researchers...","source":"Reuters"},"data.articles[1]":{"headline":"Chip Stocks Rise","body":"Semiconductor...","source":"Bloomberg"}},"metadata":{"data.articles":{"type":"array","length":42,"item_avg_bytes":310},"total_bytes":13400}}

Target parameters for "analyze_news":
- content (array) (REQUIRED): The articles to analyze
- topic (string) (REQUIRED): The research topic
- article_count (integer): Number of articles

Output:
{"content":{"source":"path","path":"data.articles","fields":["headline","body"]},"topic":{"source":"path","path":"data.topic"},"article_count":{"source":"path","path":"data.articles","transform":"count"}}
</example>

<data_structure>
%s
</data_structure>

<target_parameters capability="%s">
%s
</target_parameters>

%s
Respond with valid JSON only. Output raw JSON — no markdown, no code blocks. Start with { and end with }.`,
		string(summaryJSON),
		targetCapability.Name,
		strings.Join(paramDescs, "\n"),
		hint,
	)

	m.logDebug("Schema-guided mapping prompt", map[string]interface{}{
		"capability":    targetCapability.Name,
		"summary_bytes": len(summaryJSON),
		"prompt_bytes":  len(prompt),
	})

	// Telemetry: Record schema mapping request
	schemaEventAttrs := []attribute.KeyValue{
		attribute.String("capability", targetCapability.Name),
		attribute.String("step_id", stepID),
		attribute.Int("summary_bytes", len(summaryJSON)),
		attribute.Int("prompt_length", len(prompt)),
		attribute.String("hint", hint),
	}
	schemaOpts := mergeAIOptions(&core.AIOptions{
		Temperature: 0.0,
		MaxTokens:   m.maxTokens,
	}, m.aiOptionsOverride)

	if schemaOpts.Model != "" {
		schemaEventAttrs = append(schemaEventAttrs, attribute.String("model", schemaOpts.Model))
	}
	telemetry.AddSpanEvent(ctx, "llm.schema_mapping.request", schemaEventAttrs...)

	// Step 4: LLM call (same settings as micro-resolution: temperature=0, max_tokens=m.maxTokens)
	requestID := ""
	if baggage := telemetry.GetBaggage(ctx); baggage != nil {
		requestID = baggage["request_id"]
	}
	if requestID == "" {
		requestID = m.generateFallbackRequestID()
	}

	llmStartTime := time.Now()
	invocation := aiInvocation{
		Purpose:        "schema-mapping",
		Prompt:         prompt,
		Options:        schemaOpts,
		DeferRecording: m.debugStore != nil,
	}
	invocationResult, err := invokeAI(ctx, m.aiClient, invocation)
	var resp *core.AIResponse
	if invocationResult != nil {
		resp = invocationResult.Response
	}
	llmDuration := time.Since(llmStartTime)
	if err == nil {
		core.RecordTokenUsage(ctx, "schema_mapping", resp.Usage)
	}

	if err != nil {
		telemetry.AddSpanEvent(ctx, "llm.schema_mapping.error",
			attribute.String("capability", targetCapability.Name),
			attribute.String("error", err.Error()),
			attribute.Int64("duration_ms", llmDuration.Milliseconds()),
		)
		m.recordDebugInteraction(ctx, requestID, withEffectiveAIRequest(LLMInteraction{
			Type:       "schema_mapping",
			Timestamp:  llmStartTime,
			DurationMs: llmDuration.Milliseconds(),
			Success:    false,
			Error:      err.Error(),
			Attempt:    1,
			StepID:     stepID,
		}, invocationResult, invocation, resp, err))
		return nil, fmt.Errorf("schema-mapping LLM call failed: %w", err)
	}

	// Telemetry: Record schema mapping response
	telemetry.AddSpanEvent(ctx, "llm.schema_mapping.response",
		attribute.String("capability", targetCapability.Name),
		attribute.String("response", truncateString(resp.Content, 1000)),
		attribute.Int64("duration_ms", llmDuration.Milliseconds()),
	)
	m.recordDebugInteraction(ctx, requestID, withEffectiveAIRequest(LLMInteraction{
		Type:             "schema_mapping",
		Timestamp:        llmStartTime,
		DurationMs:       llmDuration.Milliseconds(),
		Response:         resp.Content,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		Success:          true,
		Attempt:          1,
		StepID:           stepID,
	}, invocationResult, invocation, resp, nil))

	// Step 5: Parse the mapping expressions
	content := strings.TrimSpace(resp.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var rawMapping map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &rawMapping); err != nil {
		return nil, fmt.Errorf("failed to parse schema-mapping response as JSON: %w", err)
	}

	mapping := make(map[string]MappingExpr, len(rawMapping))
	for paramName, raw := range rawMapping {
		var expr MappingExpr
		if err := json.Unmarshal(raw, &expr); err != nil {
			return nil, fmt.Errorf("failed to parse mapping expression for %q: %w", paramName, err)
		}
		// Validate source type
		switch expr.Source {
		case MappingSourcePath, MappingSourceLiteral, MappingSourceTemplate:
			// valid
		default:
			return nil, fmt.Errorf("unknown mapping source %q for parameter %q", expr.Source, paramName)
		}
		mapping[paramName] = expr
	}

	// Step 6: Apply mapping to FULL source data (no trimming, microsecond CPU)
	result, err := applyMapping(sourceData, mapping)
	if err != nil {
		return nil, fmt.Errorf("failed to apply mapping: %w", err)
	}

	// Apply type coercion to match target types (same as resolveWithText)
	for _, param := range targetCapability.Parameters {
		if val, ok := result[param.Name]; ok {
			result[param.Name] = coerceType(val, param.Type)
		}
	}

	m.logInfo("Schema-guided mapping completed", map[string]interface{}{
		"capability":      targetCapability.Name,
		"resolved_params": getMapKeys(result),
		"mapping_count":   len(mapping),
		"llm_duration_ms": llmDuration.Milliseconds(),
	})

	return result, nil
}

// applyMapping applies mapping expressions to the FULL, untrimmed source data.
// Each parameter's mapping expression tells the executor WHERE the data is and
// HOW to transform it. This is a pure, deterministic function — no LLM calls.
func applyMapping(sourceData map[string]interface{}, mapping map[string]MappingExpr) (map[string]interface{}, error) {
	result := make(map[string]interface{}, len(mapping))

	for paramName, expr := range mapping {
		var value interface{}
		var err error

		switch expr.Source {
		case MappingSourcePath:
			value = navigateToPath(sourceData, expr.Path)
			if value == nil {
				return nil, fmt.Errorf("path %q not found in source data for parameter %q", expr.Path, paramName)
			}

			// Apply field projection if specified
			if len(expr.Fields) > 0 {
				value = projectFields(value, expr.Fields)
			}

			// Apply transform if specified
			if expr.Transform != MappingTransformNone {
				value, err = applyTransform(value, expr.Transform)
				if err != nil {
					return nil, fmt.Errorf("transform %q failed for parameter %q: %w", expr.Transform, paramName, err)
				}
			}

		case MappingSourceLiteral:
			value = expr.Value

		case MappingSourceTemplate:
			value = interpolateTemplate(expr.Template, sourceData)

		default:
			return nil, fmt.Errorf("unknown mapping source %q for parameter %q", expr.Source, paramName)
		}

		result[paramName] = value
	}

	return result, nil
}

// navigateToPath traverses a nested map using a dot-separated path.
// Returns nil if any segment is not found.
//
// Design note: This intentionally does NOT support array indexing (e.g., "data.news[0]").
// The existing navigateToValue in structural_trimmer.go handles [N] indexing for trimming,
// but schema-guided mapping maps to array CONTAINERS ("data.news"), not individual items.
// For the rare case of needing a specific index, the LLM uses path + transform: "first".
// Keeping this simpler avoids importing/coupling to the trimmer's path parsing.
func navigateToPath(data map[string]interface{}, path string) interface{} {
	parts := strings.Split(path, ".")
	var current interface{} = data

	for _, part := range parts {
		if part == "" {
			continue
		}
		m, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current, ok = m[part]
		if !ok {
			return nil
		}
	}

	return current
}

// projectFields filters array items or a single object to only include the specified fields.
func projectFields(value interface{}, fields []string) interface{} {
	projectOne := func(item interface{}) interface{} {
		obj, ok := item.(map[string]interface{})
		if !ok {
			return item
		}
		projected := make(map[string]interface{}, len(fields))
		for _, f := range fields {
			if v, exists := obj[f]; exists {
				projected[f] = v
			}
		}
		return projected
	}

	if arr, ok := value.([]interface{}); ok {
		result := make([]interface{}, len(arr))
		for i, item := range arr {
			result[i] = projectOne(item)
		}
		return result
	}

	return projectOne(value)
}

// applyTransform applies a transformation to a value.
func applyTransform(value interface{}, transform MappingTransform) (interface{}, error) {
	switch transform {
	case MappingTransformSerialize:
		b, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("serialize failed: %w", err)
		}
		return string(b), nil

	case MappingTransformJoin:
		arr, ok := value.([]interface{})
		if !ok {
			return fmt.Sprintf("%v", value), nil
		}
		parts := make([]string, len(arr))
		for i, item := range arr {
			parts[i] = fmt.Sprintf("%v", item)
		}
		return strings.Join(parts, "\n"), nil

	case MappingTransformFirst:
		arr, ok := value.([]interface{})
		if !ok {
			return value, nil
		}
		if len(arr) == 0 {
			return nil, fmt.Errorf("cannot take first item from empty array")
		}
		return arr[0], nil

	case MappingTransformCount:
		arr, ok := value.([]interface{})
		if !ok {
			return nil, fmt.Errorf("count transform requires an array")
		}
		return len(arr), nil

	default:
		return nil, fmt.Errorf("unknown transform: %q", transform)
	}
}

// interpolateTemplate replaces {path} references in a template string with values from source data.
// Advances past each replacement to avoid re-scanning — replacement values containing
// curly braces are emitted verbatim without being parsed as placeholders.
func interpolateTemplate(template string, sourceData map[string]interface{}) string {
	var result strings.Builder
	remaining := template
	for {
		start := strings.Index(remaining, "{")
		if start == -1 {
			result.WriteString(remaining)
			break
		}
		end := strings.Index(remaining[start:], "}")
		if end == -1 {
			result.WriteString(remaining)
			break
		}
		end += start

		result.WriteString(remaining[:start])
		path := remaining[start+1 : end]
		val := navigateToPath(sourceData, path)
		if val != nil {
			fmt.Fprintf(&result, "%v", val)
		}
		remaining = remaining[end+1:]
	}
	return result.String()
}

// buildParameterSchema creates a JSON Schema from capability parameters
func (m *MicroResolver) buildParameterSchema(cap *EnhancedCapability) json.RawMessage {
	properties := make(map[string]interface{})
	required := []string{}

	for _, param := range cap.Parameters {
		prop := map[string]interface{}{
			"type":        mapToJSONSchemaType(param.Type),
			"description": param.Description,
		}
		properties[param.Name] = prop

		if param.Required {
			required = append(required, param.Name)
		}
	}

	schema := map[string]interface{}{
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": false, // Required for strict mode
	}

	schemaJSON, _ := json.Marshal(schema)
	return schemaJSON
}

// mapToJSONSchemaType converts Go type names to JSON Schema types
func mapToJSONSchemaType(goType string) string {
	switch strings.ToLower(goType) {
	case "number", "float", "float64", "double":
		return "number"
	case "integer", "int", "int64", "int32":
		return "integer"
	case "boolean", "bool":
		return "boolean"
	case "array", "[]string", "[]int":
		return "array"
	case "object", "map":
		return "object"
	default:
		return "string"
	}
}

// SetLogger sets the logger for the micro-resolver
// The component is always set to "framework/orchestration" to ensure proper log attribution
// regardless of which agent or tool is using the orchestration module.
func (m *MicroResolver) SetLogger(logger core.Logger) {
	if logger == nil {
		m.logger = nil
	} else {
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			m.logger = cal.WithComponent("framework/orchestration")
		} else {
			m.logger = logger
		}
	}
}

// Logging helpers
func (m *MicroResolver) logDebug(msg string, fields map[string]interface{}) {
	if m.logger != nil {
		m.logger.Debug(msg, fields)
	}
}

func (m *MicroResolver) logInfo(msg string, fields map[string]interface{}) {
	if m.logger != nil {
		m.logger.Info(msg, fields)
	}
}

func (m *MicroResolver) logWarn(msg string, fields map[string]interface{}) {
	if m.logger != nil {
		m.logger.Warn(msg, fields)
	}
}

// SetResultProcessor configures source data trimming for micro-resolution prompts.
// When set, source data exceeding maxBytes is trimmed using the provided processor
// before embedding in LLM prompts. This prevents Cloudflare 400 errors on large
// dependency results (e.g., 120 KB company_news → 139 KB prompt).
func (m *MicroResolver) SetResultProcessor(processor ResultProcessor, maxBytes int) {
	m.resultProcessor = processor
	m.maxSourceDataBytes = maxBytes
}

// trimSourceData applies ResultProcessor to the serialized source data
// before embedding it in micro-resolution prompts. Uses the target capability name
// and hint as keyword context for query-conditioned field selection.
func (m *MicroResolver) trimSourceData(
	ctx context.Context, sourceJSON []byte, targetCapability string, hint string, stepID string,
) []byte {
	// Reserve space for the prompt template that wraps the source data.
	// Without this, source data trimmed to exactly maxSourceDataBytes produces a
	// total prompt that exceeds the budget by the template overhead (~200-800 bytes),
	// causing Cloudflare 400 rejections.
	effectiveBudget := m.maxSourceDataBytes - promptTemplateReserve
	if effectiveBudget < 1024 {
		effectiveBudget = 1024 // minimum reasonable budget
	}

	if m.resultProcessor == nil || len(sourceJSON) <= effectiveBudget {
		return sourceJSON
	}

	// Build instruction context from target capability and hint
	instruction := fmt.Sprintf("Extract parameters for %s", targetCapability)
	if hint != "" {
		instruction += ". " + hint
	}

	trimmed := m.resultProcessor.ProcessForPrompt(ctx, string(sourceJSON), effectiveBudget, ResultProcessorContext{
		StepID:      stepID,
		AgentName:   "micro_resolver",
		Capability:  targetCapability,
		Instruction: instruction,
	})

	// Telemetry: record trimming event
	requestID := ""
	if bag := telemetry.GetBaggage(ctx); bag != nil {
		requestID = bag["request_id"]
	}
	telemetry.AddSpanEvent(ctx, "result_trim.micro_resolution",
		attribute.String("request_id", requestID),
		attribute.String("step_id", stepID),
		attribute.String("capability", targetCapability),
		attribute.Int("original_bytes", len(sourceJSON)),
		attribute.Int("trimmed_bytes", len(trimmed)),
		attribute.Int("configured_budget_bytes", m.maxSourceDataBytes),
		attribute.Int("effective_budget_bytes", effectiveBudget),
	)

	if m.logger != nil {
		m.logger.DebugWithContext(ctx, "Source data trimmed for micro-resolution", map[string]interface{}{
			"operation":               "result_trim_micro_resolution",
			"request_id":              requestID,
			"step_id":                 stepID,
			"capability":              targetCapability,
			"original_bytes":          len(sourceJSON),
			"trimmed_bytes":           len(trimmed),
			"configured_budget_bytes": m.maxSourceDataBytes,
			"effective_budget_bytes":  effectiveBudget,
		})
	}

	if registry := core.GetGlobalMetricsRegistry(); registry != nil {
		registry.Counter("orchestration.result_trim.micro_resolution", "capability", targetCapability)
	}

	return []byte(trimmed)
}

// structuralSummary is the compact representation of source data structure
// sent to the LLM for schema-guided mapping. Designed to be ~1-3KB regardless
// of the source data size (which can be 150KB+).
type structuralSummary struct {
	Schema   map[string]interface{} `json:"schema"`   // Type structure (types for arrays, actual values for scalars)
	Samples  map[string]interface{} `json:"samples"`  // First N items from each array
	Metadata map[string]interface{} `json:"metadata"` // Array lengths, avg sizes, total_bytes
}

// generateStructuralSummary creates a compact representation of source data structure.
// Single-pass JSON walk — same cost as json.Marshal which already happens.
func generateStructuralSummary(sourceData map[string]interface{}) structuralSummary {
	summary := structuralSummary{
		Schema:   make(map[string]interface{}),
		Samples:  make(map[string]interface{}),
		Metadata: make(map[string]interface{}),
	}

	summary.Schema = walkForSummary(sourceData, "", &summary)

	// Add total_bytes metadata
	totalJSON, _ := json.Marshal(sourceData)
	summary.Metadata["total_bytes"] = len(totalJSON)

	return summary
}

// walkForSummary recursively walks the source data to build schema, samples, and metadata.
// Returns the schema map for the current level so parent callers can embed it.
func walkForSummary(data map[string]interface{}, prefix string, summary *structuralSummary) map[string]interface{} {
	schema := make(map[string]interface{}, len(data))

	for key, val := range data {
		fullPath := key
		if prefix != "" {
			fullPath = prefix + "." + key
		}

		switch v := val.(type) {
		case map[string]interface{}:
			// Nested object: recurse and USE the returned schema (preserves full structure)
			schema[key] = walkForSummary(v, fullPath, summary)

		case []interface{}:
			// Array: record item schema from representative element, sample items, record size metadata
			if len(v) > 0 {
				// Schema: array notation with item type/schema from representative element
				// Uses representativeElement to skip leading nulls common in time-series APIs
				schema[key] = []interface{}{typeSchemaForValue(representativeElement(v))}

				// Samples: first maxSummarySamples non-nil items (gives LLM concrete examples)
				sampled := 0
				for i := 0; i < len(v) && sampled < maxSummarySamples; i++ {
					if v[i] != nil {
						sampleKey := fmt.Sprintf("%s[%d]", fullPath, i)
						summary.Samples[sampleKey] = v[i]
						sampled++
					}
				}
				// Fallback: if all elements are nil, sample the first N anyway
				if sampled == 0 {
					sampleCount := maxSummarySamples
					if len(v) < sampleCount {
						sampleCount = len(v)
					}
					for i := 0; i < sampleCount; i++ {
						sampleKey := fmt.Sprintf("%s[%d]", fullPath, i)
						summary.Samples[sampleKey] = v[i]
					}
				}

				// Metadata: array length + average item size
				totalBytes := 0
				for _, item := range v {
					b, _ := json.Marshal(item)
					totalBytes += len(b)
				}
				summary.Metadata[fullPath] = map[string]interface{}{
					"type":           "array",
					"length":         len(v),
					"item_avg_bytes": totalBytes / len(v),
				}
			} else {
				schema[key] = []interface{}{} // empty array
			}

		default:
			// Scalar: record actual value if small enough for the LLM to use in
			// template interpolation and literal inference. Large strings (base64,
			// HTML, logs) get a type+size descriptor instead — the LLM only needs
			// the path reference, not the content.
			if s, ok := val.(string); ok && len(s) > maxScalarSchemaBytes {
				schema[key] = fmt.Sprintf("string(%d bytes)", len(s))
			} else {
				schema[key] = val
			}
		}
	}

	// Collapse structurally identical siblings at THIS level.
	// Runs for every map level including the root. Since walkForSummary recurses
	// depth-first for map children, each child's schema is already deduplicated
	// before this level's collapseSiblings runs.
	schema, _ = collapseSiblings(schema, data, prefix, summary)

	return schema
}

// representativeElement returns the first non-nil element from an array,
// falling back to the first element if all elements are nil.
// This handles APIs that return null for missing data at the start of
// time-series arrays (e.g., weather, sensor readings, financial data).
func representativeElement(arr []interface{}) interface{} {
	for _, item := range arr {
		if item != nil {
			return item
		}
	}
	if len(arr) > 0 {
		return arr[0]
	}
	return nil
}

// structuralFingerprint computes a canonical string describing the structural
// shape of a JSON value. Two values with identical fingerprints have the same
// type tree (same keys, same nesting, same leaf types). Used by collapseSiblings
// to detect groups of structurally identical siblings.
func structuralFingerprint(val interface{}) string {
	switch v := val.(type) {
	case map[string]interface{}:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = k + ":" + structuralFingerprint(v[k])
		}
		return "object:{" + strings.Join(parts, ",") + "}"
	case []interface{}:
		if len(v) > 0 {
			rep := representativeElement(v)
			return "array:" + structuralFingerprint(rep)
		}
		return "array:empty"
	default:
		return typeString(v)
	}
}

// typeString returns a human-readable type string for a JSON value.
// Used in typeSchemaForValue for array item schemas where we want types, not values.
func typeString(val interface{}) string {
	switch val.(type) {
	case string:
		return "string"
	case float64, float32, int, int64, int32, json.Number:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	case map[string]interface{}:
		return "object"
	case []interface{}:
		return "array"
	default:
		return "unknown"
	}
}

// typeSchemaForValue returns a schema representation for an array item.
// For objects, returns a map of field names to type strings (showing structure).
// For scalars, returns the type string.
// Note: array item schemas use TYPES (not values) because items repeat —
// the LLM sees actual values in the samples section instead.
func typeSchemaForValue(val interface{}) interface{} {
	if obj, ok := val.(map[string]interface{}); ok {
		schema := make(map[string]interface{}, len(obj))
		for k, v := range obj {
			schema[k] = typeString(v)
		}
		return schema
	}
	return typeString(val)
}

// collapsedGroup tracks a set of siblings that were collapsed for post-processing.
type collapsedGroup struct {
	prefix         string   // Parent path (e.g., "data.series.annual")
	keys           []string // All keys in the group (sorted)
	fingerprint    string   // Shared structural fingerprint
	representative string   // The key whose samples are kept
}

// collapseSiblings detects groups of structurally identical children in a
// schema map and replaces each group with a single self-describing entry.
//
// Returns the deduplicated schema and a list of collapsed groups.
//
// Schema placement depends on the result:
//   - Single group, all children collapsed: returns the group entry directly
//     (parent map becomes the grouped entry).
//   - Multiple groups or mixed: returns schema with _group_N keys alongside
//     ungrouped children.
func collapseSiblings(
	schema map[string]interface{},
	data map[string]interface{},
	prefix string,
	summary *structuralSummary,
) (map[string]interface{}, []collapsedGroup) {
	// Step 1: Fingerprint each child using the ORIGINAL data (not the schema)
	fingerprints := make(map[string][]string) // fingerprint → list of keys
	for key, val := range data {
		// Only fingerprint arrays and objects — scalars are not collapsed
		switch val.(type) {
		case []interface{}, map[string]interface{}:
			fp := structuralFingerprint(val)
			fingerprints[fp] = append(fingerprints[fp], key)
		}
	}

	// Step 2: Identify groups meeting the threshold
	var groups []collapsedGroup
	groupIdx := 0 // Incrementing counter for collision-free key naming

	for fp, keys := range fingerprints {
		if len(keys) < minSiblingGroupSize {
			continue
		}
		sort.Strings(keys)

		// Build the grouped schema entry
		representative := data[keys[0]]
		groupEntry := map[string]interface{}{
			"_grouped": true,
			"_count":   len(keys),
		}

		// _element_schema: matches the format walkForSummary already produces
		// Uses representativeElement for arrays to handle null-first-element case
		switch v := representative.(type) {
		case []interface{}:
			if len(v) > 0 {
				groupEntry["_element_schema"] = []interface{}{typeSchemaForValue(representativeElement(v))}
			} else {
				groupEntry["_element_schema"] = []interface{}{}
			}
		case map[string]interface{}:
			groupEntry["_element_schema"] = typeSchemaForValue(v)
		}

		// _keys: truncated list with sentinel
		if len(keys) <= maxGroupedKeys {
			groupEntry["_keys"] = keys
		} else {
			truncated := make([]interface{}, maxGroupedKeys+1)
			for i := 0; i < maxGroupedKeys; i++ {
				truncated[i] = keys[i]
			}
			truncated[maxGroupedKeys] = fmt.Sprintf("...+%d more", len(keys)-maxGroupedKeys)
			groupEntry["_keys"] = truncated
		}

		// Remove individual entries from schema, replace with grouped entry.
		// Uses incrementing counter for collision-free naming.
		for _, key := range keys {
			delete(schema, key)
		}
		schema[fmt.Sprintf("_group_%d", groupIdx)] = groupEntry
		groupIdx++

		groups = append(groups, collapsedGroup{
			prefix:         prefix,
			keys:           keys,
			fingerprint:    fp,
			representative: keys[0],
		})
	}

	if len(groups) == 0 {
		return schema, nil
	}

	// Step 3: Clean up samples and metadata for collapsed children
	for groupNum, g := range groups {
		for _, key := range g.keys {
			fullPath := key
			if g.prefix != "" {
				fullPath = g.prefix + "." + key
			}

			if key == g.representative {
				continue // Keep the representative member's samples
			}

			// Remove this member's samples (metadata is cleaned up later
			// in the aggregate computation loop — deleting it here would
			// prevent the aggregate from reading all members' values)
			for sampleKey := range summary.Samples {
				if strings.HasPrefix(sampleKey, fullPath+"[") || strings.HasPrefix(sampleKey, fullPath+".") {
					delete(summary.Samples, sampleKey)
				}
			}
		}

		// Replace individual metadata with aggregate metadata.
		// When multiple groups exist under the same parent, each group gets
		// a distinct metadata key to prevent silent overwrites.
		groupMetadataKey := g.prefix
		if len(groups) > 1 {
			// Multi-group: per-group metadata key
			if g.prefix != "" {
				groupMetadataKey = fmt.Sprintf("%s._group_%d", g.prefix, groupNum)
			} else {
				groupMetadataKey = fmt.Sprintf("_group_%d", groupNum)
			}
		} else if g.prefix == "" {
			// Single group at root level
			groupMetadataKey = "_group"
		}

		// Compute aggregate metadata across all members
		switch data[g.representative].(type) {
		case []interface{}:
			minLen, maxLen := math.MaxInt64, 0
			minAvg, maxAvg := math.MaxInt64, 0
			for _, key := range g.keys {
				fullPath := key
				if g.prefix != "" {
					fullPath = g.prefix + "." + key
				}
				if meta, ok := summary.Metadata[fullPath]; ok {
					if m, ok := meta.(map[string]interface{}); ok {
						if l, ok := m["length"].(int); ok {
							if l < minLen {
								minLen = l
							}
							if l > maxLen {
								maxLen = l
							}
						}
						if a, ok := m["item_avg_bytes"].(int); ok {
							if a < minAvg {
								minAvg = a
							}
							if a > maxAvg {
								maxAvg = a
							}
						}
					}
				}
				// Remove individual metadata (including representative)
				delete(summary.Metadata, fullPath)
			}
			// Clamp sentinel values when no member had metadata
			if minLen == math.MaxInt64 {
				minLen, maxLen = 0, 0
				minAvg, maxAvg = 0, 0
			}
			summary.Metadata[groupMetadataKey] = map[string]interface{}{
				"type":                 "grouped_arrays",
				"group_count":          len(g.keys),
				"length_range":         []int{minLen, maxLen},
				"item_avg_bytes_range": []int{minAvg, maxAvg},
			}
		case map[string]interface{}:
			// For grouped objects, compute avg_bytes range
			minBytes, maxBytes := math.MaxInt64, 0
			for _, key := range g.keys {
				b, _ := json.Marshal(data[key])
				sz := len(b)
				if sz < minBytes {
					minBytes = sz
				}
				if sz > maxBytes {
					maxBytes = sz
				}
			}
			summary.Metadata[groupMetadataKey] = map[string]interface{}{
				"type":            "grouped_objects",
				"group_count":     len(g.keys),
				"avg_bytes_range": []int{minBytes, maxBytes},
			}
		}
	}

	// Step 4: Unwrap single group when all children are collapsed.
	// When exactly one group consumed all children, the parent map directly
	// contains the grouped metadata instead of wrapping in _group_0.
	if len(groups) == 1 {
		hasUngrouped := false
		for k := range schema {
			if !strings.HasPrefix(k, "_group_") {
				hasUngrouped = true
				break
			}
		}
		if !hasUngrouped {
			return schema["_group_0"].(map[string]interface{}), groups
		}
	}

	return schema, groups
}

// SetSchemaMappingThreshold configures the source data size threshold for
// schema-guided mapping. Source data above this size uses schema-guided mapping
// instead of value extraction. Set to 0 to disable schema-guided mapping.
func (m *MicroResolver) SetSchemaMappingThreshold(threshold int) {
	m.schemaMappingThreshold = threshold
}

// SetLLMDebugStore sets the LLM debug store for full payload visibility.
func (m *MicroResolver) SetLLMDebugStore(store LLMDebugStore) {
	m.debugStore = store
}

// recordDebugInteraction stores an LLM interaction for debugging.
// Uses WaitGroup to track in-flight recordings for graceful shutdown.
func (m *MicroResolver) recordDebugInteraction(ctx context.Context, requestID string, interaction LLMInteraction) {
	if m.debugStore == nil {
		return
	}

	m.debugWg.Add(1)
	go func() {
		defer m.debugWg.Done()

		recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 1*time.Second)
		defer cancel()

		if err := m.debugStore.RecordInteraction(recordCtx, requestID, interaction); err != nil {
			m.logWarn("Failed to record LLM debug interaction", map[string]interface{}{
				"request_id": requestID,
				"type":       interaction.Type,
				"error":      err.Error(),
			})
		}
	}()
}

// generateFallbackRequestID generates a request ID when TraceID is not available.
func (m *MicroResolver) generateFallbackRequestID() string {
	seq := m.debugSeqID.Add(1)
	return fmt.Sprintf("micro-%d-%d", time.Now().UnixNano(), seq)
}

// Shutdown waits for pending debug recordings to complete.
func (m *MicroResolver) Shutdown(ctx context.Context) error {
	done := make(chan struct{})
	go func() {
		m.debugWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
