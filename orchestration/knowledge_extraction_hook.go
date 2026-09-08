package orchestration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/truvaagents/truva-g3/core"
	"github.com/truvaagents/truva-g3/telemetry"
	"go.opentelemetry.io/otel/attribute"
)

// Compile-time interface compliance check.
var _ core.AfterSynthesisHook = (*KnowledgeExtractionHook)(nil)

// KnowledgeExtractionHook implements core.AfterSynthesisHook.
// It extracts knowledge fragments from completed orchestration executions
// using an LLM, embeds them, and stores them in SharedKnowledge for future use.
//
// Runs asynchronously (fire-and-forget) to avoid adding latency to the response path.
// Errors are logged but never propagate — fail-open by design.
type KnowledgeExtractionHook struct {
	knowledge   core.SharedKnowledge
	embedder    core.EmbeddingClient
	aiClient    core.AIClient
	agentDomain string
	agentName   string
	logger      core.Logger
	wg          sync.WaitGroup     // Tracks background extraction goroutines for graceful shutdown
	cancelFunc  context.CancelFunc // Cancels in-flight extractions on shutdown
	shutdownCtx context.Context    // Context for background goroutines — cancelled on Close()
}

// KnowledgeExtractionOption configures KnowledgeExtractionHook.
// Returns error if the option value is invalid (fail-fast per core/ARCHITECTURE.md).
type KnowledgeExtractionOption func(*KnowledgeExtractionHook) error

// WithExtractionLogger sets the logger.
func WithExtractionLogger(logger core.Logger) KnowledgeExtractionOption {
	return func(h *KnowledgeExtractionHook) error {
		if logger == nil {
			return fmt.Errorf("logger cannot be nil: use &core.NoOpLogger{} to disable logging")
		}
		if cal, ok := logger.(core.ComponentAwareLogger); ok {
			h.logger = cal.WithComponent("framework/orchestration")
		} else {
			h.logger = logger
		}
		return nil
	}
}

// NewKnowledgeExtractionHook creates a knowledge extraction hook.
// All three parameters (knowledge, embedder, aiClient) are required.
func NewKnowledgeExtractionHook(
	knowledge core.SharedKnowledge,
	embedder core.EmbeddingClient,
	aiClient core.AIClient,
	agentName string,
	agentDomain string,
	opts ...KnowledgeExtractionOption,
) (*KnowledgeExtractionHook, error) {
	if knowledge == nil {
		return nil, fmt.Errorf("knowledge store is required for KnowledgeExtractionHook")
	}
	if embedder == nil {
		return nil, fmt.Errorf("embedding client is required for KnowledgeExtractionHook")
	}
	if aiClient == nil {
		return nil, fmt.Errorf("AI client is required for KnowledgeExtractionHook")
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &KnowledgeExtractionHook{
		knowledge:   knowledge,
		embedder:    embedder,
		aiClient:    aiClient,
		agentDomain: agentDomain,
		agentName:   agentName,
		logger:      &core.NoOpLogger{},
		shutdownCtx: ctx,
		cancelFunc:  cancel,
	}
	for _, opt := range opts {
		if err := opt(h); err != nil {
			cancel() // Clean up context if option fails
			return nil, fmt.Errorf("invalid extraction hook option: %w", err)
		}
	}
	return h, nil
}

// Close cancels in-flight extraction goroutines and waits for them to complete.
// Per FRAMEWORK_DESIGN_PRINCIPLES §Performance: "Goroutines: Must clean up on shutdown."
func (h *KnowledgeExtractionHook) Close() {
	h.cancelFunc() // Signal all in-flight extractions to stop
	h.wg.Wait()    // Wait for them to finish
}

// Name returns the hook name for telemetry spans.
func (h *KnowledgeExtractionHook) Name() string { return "knowledge-extraction" }

// AfterSynthesis extracts knowledge from the synthesized response and stores it.
// Runs the extraction asynchronously to avoid blocking the response.
func (h *KnowledgeExtractionHook) AfterSynthesis(ctx context.Context, pctx *core.PipelineContext, response string) (string, error) {
	effectStart := time.Now()
	if response == "" || h.knowledge == nil || h.embedder == nil || h.aiClient == nil {
		reportStructuredPipelineHookEffect(
			ctx,
			"knowledge_extraction",
			"Knowledge extraction",
			core.PipelineHookEffectSkipped,
			"Knowledge extraction was not applicable",
			nil,
			nil,
			effectStart,
		)
		return response, nil // Nothing to extract or no storage configured
	}

	requestID := GetRequestID(ctx)
	traceContext := telemetry.GetTraceContext(ctx)
	reportStructuredPipelineHookEffect(
		ctx,
		"knowledge_extraction",
		"Knowledge extraction",
		core.PipelineHookEffectPending,
		"Asynchronous knowledge extraction is scheduled",
		struct {
			Asynchronous bool `json:"asynchronous"`
		}{Asynchronous: true},
		nil,
		effectStart,
	)
	// The extraction outlives the request, so it must not retain the request's
	// cancellation or parent span. Copy bounded correlation baggage onto the
	// hook-owned shutdown context and start a linked root span in the goroutine.
	backgroundCtx := telemetry.CopyBaggage(h.shutdownCtx, ctx)
	backgroundCtx = core.WithRequestID(backgroundCtx, requestID)
	if reporter, ok := core.PipelineHookEffectReporterFromContext(ctx); ok {
		backgroundCtx = core.WithPipelineHookEffectReporter(backgroundCtx, reporter)
	}
	originalRequest := pctx.Request

	// Run extraction asynchronously — tracked by WaitGroup, cancellable via shutdownCtx
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		asyncCtx, endSpan := telemetry.StartLinkedSpan(
			backgroundCtx,
			"pipeline.hook.async.knowledge-extraction",
			traceContext.TraceID,
			traceContext.SpanID,
			map[string]string{
				"request_id":          requestID,
				"pipeline.hook.name":  h.Name(),
				"pipeline.hook.phase": PipelineHookPhaseAfterSynthesis,
			},
		)
		defer endSpan()
		h.extractAndStore(asyncCtx, originalRequest, response, requestID, effectStart)
	}()

	return response, nil // Response is never mutated
}

// extractAndStore performs the actual LLM extraction, embedding, and storage.
// Runs in a background goroutine — all errors are logged, never propagated.
func (h *KnowledgeExtractionHook) extractAndStore(
	ctx context.Context,
	originalRequest string,
	synthesizedResponse string,
	requestID string,
	effectStart time.Time,
) {
	startTime := time.Now()
	effectStatus := core.PipelineHookEffectSkipped
	effectSummary := "No reusable knowledge fragments were extracted"
	var effectErr error
	effectData := knowledgeExtractionEffectData{}
	defer func() {
		reportStructuredPipelineHookEffect(
			ctx,
			"knowledge_extraction",
			"Knowledge extraction",
			effectStatus,
			effectSummary,
			effectData,
			effectErr,
			effectStart,
		)
	}()

	// 1. Ask LLM to extract learnings from the execution
	extractionPrompt := fmt.Sprintf(`Extract 0-3 reusable knowledge fragments from this completed agent execution.
Each fragment should be a concise, actionable insight that would help future agents handling similar situations.

Only extract knowledge if there is a genuinely reusable pattern. If the execution was routine with no novel insights, return an empty array.

Original request: %s

Synthesized response: %s

Return a JSON array of objects with these fields:
- "content": string — the knowledge statement (1-2 sentences)
- "namespace": string — category: "incidents", "runbooks", "decisions", or "patterns"
- "importance": number — 1.0 to 10.0 significance rating

Example: [{"content": "High latency on Go services with tight memory limits is caused by GC pressure exhausting connection pools. Fix: increase memory limit to 256Mi.", "namespace": "incidents", "importance": 8.0}]

Return [] if no reusable knowledge.`, originalRequest, truncateForExtraction(synthesizedResponse, 3000))

	invocationResult, err := invokeAI(ctx, h.aiClient, aiInvocation{
		Purpose: "knowledge-extraction",
		Prompt:  extractionPrompt,
		Options: &core.AIOptions{
			Temperature: 0.3,
			MaxTokens:   1000,
		},
	})
	if err != nil {
		effectStatus = core.PipelineHookEffectFailed
		effectSummary = "Knowledge extraction LLM call failed"
		effectErr = err
		recordPipelineHookEffectSpanError(ctx, "llm_unavailable")
		if h.logger != nil {
			h.logger.WarnWithContext(ctx, "Knowledge extraction LLM call failed", map[string]interface{}{
				"operation":  "knowledge_extraction",
				"request_id": requestID,
				"error_type": "llm_unavailable",
			})
		}
		return
	}
	aiResp := invocationResult.Response
	effectData.ModelResponse = aiResp.Content

	// 2. Parse LLM response
	var fragments []extractedFragment
	if err := json.Unmarshal([]byte(aiResp.Content), &fragments); err != nil {
		// Try to find JSON array in the response (LLM may add prose around it)
		if extracted := extractJSONArray(aiResp.Content); extracted != "" {
			if err := json.Unmarshal([]byte(extracted), &fragments); err != nil {
				effectStatus = core.PipelineHookEffectFailed
				effectSummary = "Knowledge extraction response could not be parsed"
				effectErr = err
				telemetry.RecordSpanError(ctx, err)
				if h.logger != nil {
					h.logger.WarnWithContext(ctx, "Failed to parse knowledge extraction response", map[string]interface{}{
						"operation":  "knowledge_extraction",
						"request_id": requestID,
						"error":      err.Error(),
						"error_type": "parse_failure",
					})
				}
				return
			}
		} else {
			effectStatus = core.PipelineHookEffectFailed
			effectSummary = "Knowledge extraction response contained no JSON array"
			effectErr = fmt.Errorf("knowledge extraction response contained no JSON array")
			telemetry.RecordSpanError(ctx, effectErr)
			return // No parseable fragments
		}
	}

	if len(fragments) == 0 {
		return // LLM determined no reusable knowledge
	}

	// 3. Embed and store each fragment
	storedCount := 0
	for _, f := range fragments {
		if f.Content == "" {
			effectData.Fragments = append(effectData.Fragments, knowledgeFragmentEffect{
				Namespace:       f.Namespace,
				Importance:      f.Importance,
				Status:          core.PipelineHookEffectSkipped,
				ObservedOutcome: "empty_content",
				Error:           "content is empty",
			})
			continue
		}
		fragmentEffect := knowledgeFragmentEffect{
			Content:         f.Content,
			Namespace:       f.Namespace,
			Importance:      f.Importance,
			Status:          core.PipelineHookEffectPending,
			ObservedOutcome: "embedding_pending",
		}

		// Generate embedding
		embResp, embErr := h.embedder.GenerateEmbeddings(ctx, []string{f.Content}, nil)
		if embErr != nil || len(embResp.Embeddings) == 0 || len(embResp.Embeddings[0]) == 0 {
			if embErr == nil {
				embErr = fmt.Errorf("embedding provider returned no vector")
			}
			fragmentEffect.Status = core.PipelineHookEffectFailed
			fragmentEffect.ObservedOutcome = "embedding_failed"
			fragmentEffect.Error = embErr.Error()
			if effectErr == nil {
				effectErr = embErr
			}
			effectData.Fragments = append(effectData.Fragments, fragmentEffect)
			recordPipelineHookEffectSpanError(ctx, "embedding")
			if h.logger != nil {
				h.logger.WarnWithContext(ctx, "Failed to embed knowledge fragment", map[string]interface{}{
					"operation":  "knowledge_extraction",
					"request_id": requestID,
					"error_type": "embedding",
				})
			}
			continue
		}

		// Record embedding token usage for billing attribution
		core.RecordTokenUsage(ctx, "knowledge_embedding", embResp.Usage)

		namespace := f.Namespace
		if namespace == "" {
			namespace = "patterns"
		}
		importance := f.Importance
		if importance <= 0 {
			importance = 5.0
		}
		fragmentEffect.Namespace = namespace
		fragmentEffect.Importance = importance

		fragment := core.KnowledgeFragment{
			Namespace:    namespace,
			Content:      f.Content,
			Embedding:    embResp.Embeddings[0],
			SourceEvents: []string{requestID},
			AgentDomain:  h.agentDomain,
			Scope:        core.ScopeSharedDomain,
			Importance:   importance,
			CreatedAt:    time.Now(),
		}

		if err := h.knowledge.StoreKnowledge(ctx, fragment); err != nil {
			fragmentEffect.Status = core.PipelineHookEffectFailed
			fragmentEffect.ObservedOutcome = "provider_call_returned_error"
			fragmentEffect.Error = err.Error()
			if effectErr == nil {
				effectErr = err
			}
			effectData.Fragments = append(effectData.Fragments, fragmentEffect)
			recordPipelineHookEffectSpanError(ctx, "knowledge_store")
			if h.logger != nil {
				h.logger.WarnWithContext(ctx, "Failed to store knowledge fragment", map[string]interface{}{
					"operation":  "knowledge_extraction",
					"request_id": requestID,
					"error_type": "knowledge_store",
				})
			}
			continue
		}
		fragmentEffect.Status = core.PipelineHookEffectSucceeded
		fragmentEffect.ObservedOutcome = "provider_call_returned_without_error"
		effectData.Fragments = append(effectData.Fragments, fragmentEffect)
		storedCount++
	}

	failedCount := 0
	for _, fragment := range effectData.Fragments {
		if fragment.Status == core.PipelineHookEffectFailed {
			failedCount++
		}
	}
	switch {
	case len(effectData.Fragments) == 0:
		effectStatus = core.PipelineHookEffectSkipped
	case storedCount == 0 && failedCount > 0:
		effectStatus = core.PipelineHookEffectFailed
		effectSummary = "Every shared-knowledge provider call returned an error"
	case failedCount > 0:
		effectStatus = core.PipelineHookEffectPartial
		effectSummary = fmt.Sprintf("Provider calls accepted %d of %d extracted knowledge fragments", storedCount, len(effectData.Fragments))
	default:
		effectStatus = core.PipelineHookEffectSucceeded
		effectSummary = fmt.Sprintf("Submitted %d reusable knowledge fragment(s) to the fail-open provider", storedCount)
	}

	if storedCount > 0 {
		if h.logger != nil {
			h.logger.InfoWithContext(ctx, "Knowledge fragments extracted and stored", map[string]interface{}{
				"operation":        "knowledge_extraction",
				"request_id":       requestID,
				"fragments_stored": storedCount,
				"duration_ms":      time.Since(startTime).Milliseconds(),
			})
		}

		telemetry.AddSpanEvent(ctx, "memory.knowledge.extracted",
			attribute.String("request_id", requestID),
			attribute.Int("fragments_stored", storedCount),
		)
		telemetry.Counter("memory.knowledge.fragments_stored",
			"module", telemetry.ModuleOrchestration,
			"agent", h.agentName,
		)
	}
}

// --- Helper types and functions ---

type extractedFragment struct {
	Content    string  `json:"content"`
	Namespace  string  `json:"namespace"`
	Importance float64 `json:"importance"`
}

type knowledgeExtractionEffectData struct {
	ModelResponse string                    `json:"model_response,omitempty"`
	Fragments     []knowledgeFragmentEffect `json:"fragments"`
}

type knowledgeFragmentEffect struct {
	Content         string                        `json:"content,omitempty"`
	Namespace       string                        `json:"namespace,omitempty"`
	Importance      float64                       `json:"importance,omitempty"`
	Status          core.PipelineHookEffectStatus `json:"status"`
	ObservedOutcome string                        `json:"observed_outcome"`
	Error           string                        `json:"error,omitempty"`
}

// truncateForExtraction limits text length for LLM prompts.
func truncateForExtraction(text string, maxChars int) string {
	if len(text) <= maxChars {
		return text
	}
	return text[:maxChars] + "...[truncated]"
}

// extractJSONArray finds the first JSON array in a string.
// Handles LLM responses that include prose around the JSON.
func extractJSONArray(text string) string {
	start := -1
	depth := 0
	for i, ch := range text {
		switch ch {
		case '[':
			if depth == 0 {
				start = i
			}
			depth++
		case ']':
			depth--
			if depth == 0 && start >= 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}
